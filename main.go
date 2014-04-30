package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"unicode"
)

var debugFile string
var logFile string
var cmdsFile string

type Header struct {
	Version     int  `json:"version"`
	StopSignal  int  `json:"stop_signal,omitempty"`
	ContSignal  int  `json:"cont_signal,omitempty"`
	ClickEvents bool `json:"click_events,omitempty"`
}

type Block struct {
	FullText string `json:"full_text"`
}

func (b Block) String() string {
	return b.FullText
}

type CmdIO struct {
	// Cmd is the command being run
	Cmd    *exec.Cmd
	reader io.ReadCloser
}

type BlockAggregate struct {
	CmdIO  *CmdIO
	Blocks []*Block
}

func NewCmdIO(c string) (*CmdIO, error) {
	cmd := exec.Command("sh", "-c", c)
	reader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	cmdio := CmdIO{
		Cmd:    cmd,
		reader: reader,
	}
	return &cmdio, nil
}

func (c *CmdIO) Start(blockAggregatesCh chan<- *BlockAggregate) error {
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	go func() {
		// We'll handle a few cases here.
		// If JSON is output from i3status, then we need
		// to ignore the i3bar header and opening [,
		// then ignore leading comma on each line.
		// If JSON is output from a script, it assumes the
		// author will not have the header and [, but maybe the comma
		r := bufio.NewReader(c.reader)
		// try Read a header first
		ruune, _, err := r.ReadRune()
		if err != nil {
			log.Println(err)
			return
		}
		if ruune == '{' {
			// Consume the header line
			if _, err := r.ReadString('\n'); err != nil {
				log.Println(err)
				return
			}
			// Consume the next line (opening bracket)
			if _, err := r.ReadString('\n'); err != nil {
				log.Println(err)
				return
			}
		} else {
			r.UnreadRune()
		}
		dec := json.NewDecoder(r)
		for {
			var b []*Block
			// Ignore unwanted chars first
		IgnoreChars:
			for {
				ruune, _, err := r.ReadRune()
				if err != nil {
					log.Println(err)
					continue
				}
				switch {
				case unicode.IsSpace(ruune):
					// Loop again
				case ruune == ',':
					break IgnoreChars
				default:
					r.UnreadRune()
					break IgnoreChars
				}
			}
			if err := dec.Decode(&b); err != nil {
				log.Printf("Invalid JSON input: all decoding methods failed (%v)\n", err)
			}
			blockAggregatesCh <- &BlockAggregate{c, b}
		}
		c.reader.Close()
	}()
	return nil
}

type BlockAggregator struct {
	Blocks map[*CmdIO][]*Block
	CmdIOs []*CmdIO
	w      io.Writer
}

func NewBlockAggregator(w io.Writer) *BlockAggregator {
	return &BlockAggregator{
		Blocks: make(map[*CmdIO][]*Block),
		CmdIOs: make([]*CmdIO, 0),
		w:      w,
	}
}

func (ba *BlockAggregator) Aggregate(blockAggregates <-chan *BlockAggregate) {
	jw := json.NewEncoder(ba.w)
	for blockAggregate := range blockAggregates {
		ba.Blocks[blockAggregate.CmdIO] = blockAggregate.Blocks
		blocksUpdate := make([]*Block, 0)
		for _, cmdio := range ba.CmdIOs {
			blocksUpdate = append(blocksUpdate, ba.Blocks[cmdio]...)
		}
		if err := jw.Encode(blocksUpdate); err != nil {
			log.Println(err)
		}
		ba.w.Write([]byte(","))
	}
}

func init() {
	flag.StringVar(&debugFile, "debug-file", "", "Outputs JSON to this file as well -- for debugging")
	flag.StringVar(&logFile, "log-file", "", "Log i3cat events in this file")
	flag.StringVar(&cmdsFile, "cmd-file", "$HOME/.i3/i3cat.conf", "File listing of the commands to run")
	flag.Parse()
}

func main() {
	var cmdsReader io.ReadCloser
	if cmdsFile == "-" {
		cmdsReader = ioutil.NopCloser(os.Stdin)
	} else {
		f, err := os.Open(os.ExpandEnv(cmdsFile))
		if err != nil {
			log.Fatal(err)
		}
		cmdsReader = f
	}
	commands := make([]string, 0)
	scanner := bufio.NewScanner(cmdsReader)
	for scanner.Scan() {
		cmd := strings.TrimSpace(scanner.Text())
		if cmd != "" && !strings.HasPrefix(cmd, "#") {
			commands = append(commands, cmd)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
	cmdsReader.Close()

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0660)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	var out io.Writer
	if debugFile != "" {
		f, err := os.OpenFile(debugFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0660)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
	} else {
		out = os.Stdout
	}

	header := Header{1, 10, 12, true}
	hb, err := json.Marshal(header)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(out, "%s\n[\n", hb)

	blocksCh := make(chan *BlockAggregate)
	ba := NewBlockAggregator(out)

	for _, c := range commands {
		cmdio, err := NewCmdIO(c)
		if err != nil {
			log.Fatal(err)
		}
		ba.CmdIOs = append(ba.CmdIOs, cmdio)
		if err := cmdio.Start(blocksCh); err != nil {
			log.Fatal(err)
		}
	}

	go ba.Aggregate(blocksCh)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	for {
		// TODO handle sigcont and sigstop received from i3bar, and forward to cmds
		s := <-c
		switch s {
		case os.Interrupt:
			// Kill all processes on interrupt
			log.Println("SIGINT received: terminating all processes...")
			for _, cmdio := range ba.CmdIOs {
				if err := cmdio.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
					log.Println(err)
					if err := cmdio.Cmd.Process.Kill(); err != nil {
						log.Println(err)
					}
				}
			}
			os.Exit(0)
		}
	}
}
