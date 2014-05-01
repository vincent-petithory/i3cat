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
var header Header

// Header defines the struct of the header in the i3bar protocol.
type Header struct {
	Version     int  `json:"version"`
	StopSignal  int  `json:"stop_signal,omitempty"`
	ContSignal  int  `json:"cont_signal,omitempty"`
	ClickEvents bool `json:"click_events,omitempty"`
}

// Block defines the struct of blocks in the i3bar protocol.
type Block struct {
	FullText            string `json:"full_text"`
	ShortText           string `json:"short_text,omitempty"`
	Color               string `json:"color,omitempty"`
	MinWidth            int    `json:"min_width,omitempty"`
	Align               string `json:"align,omitempty"`
	Name                string `json:"name,omitempty"`
	Instance            string `json:"instance,omitempty"`
	Urgent              bool   `json:"urgent,omitempty"`
	Separator           bool   `json:"separator,omitempty"`
	SeparatorBlockWidth int    `json:"separator_block_width,omitempty"`
}

// String implements Stringer interface.
func (b Block) String() string {
	return b.FullText
}

// A CmdIO defines a cmd that will feed the i3bar.
type CmdIO struct {
	// Cmd is the command being run
	Cmd *exec.Cmd
	// reader is the underlying stream where Cmd outputs data.
	reader io.ReadCloser
}

// BlockAggregate relates a CmdIO to the Blocks it produced during one update.
type BlockAggregate struct {
	CmdIO  *CmdIO
	Blocks []*Block
}

// NewCmdIO creates a new CmdIO from command c.
// c must be properly quoted for a shell as it's passed to sh -c.
func NewCmdIO(c string) (*CmdIO, error) {
	cmd := exec.Command(os.Getenv("SHELL"), "-c", c)
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

// Start runs the command of CmdIO and feeds the BlockAggregatesCh channel
// with the Blocks it produces.
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

// BlockAggregator fans-in all Blocks produced by a list of CmdIO and sends it to the writer W.
type BlockAggregator struct {
	// Blocks keeps track of which CmdIO produced which Block list.
	Blocks map[*CmdIO][]*Block
	// CmdIOs keeps an ordered list of the CmdIOs being aggregated.
	CmdIOs []*CmdIO
	// W is where multiplexed input blocks are written to.
	W io.Writer
}

// NewBlockAggregator returns a BlockAggregator which will write to w.
func NewBlockAggregator(w io.Writer) *BlockAggregator {
	return &BlockAggregator{
		Blocks: make(map[*CmdIO][]*Block),
		CmdIOs: make([]*CmdIO, 0),
		W:      w,
	}
}

// Aggregate starts aggregating data coming from the BlockAggregates channel.
func (ba *BlockAggregator) Aggregate(blockAggregates <-chan *BlockAggregate) {
	jw := json.NewEncoder(ba.W)
	for blockAggregate := range blockAggregates {
		ba.Blocks[blockAggregate.CmdIO] = blockAggregate.Blocks
		blocksUpdate := make([]*Block, 0)
		for _, cmdio := range ba.CmdIOs {
			blocksUpdate = append(blocksUpdate, ba.Blocks[cmdio]...)
		}
		if err := jw.Encode(blocksUpdate); err != nil {
			log.Println(err)
		}
		ba.W.Write([]byte(","))
	}
}

func init() {
	flag.StringVar(&debugFile, "debug-file", "", "Outputs JSON to this file as well; for debugging what is sent to i3bar.")
	flag.StringVar(&logFile, "log-file", "", "Logs i3cat events in this file. Defaults to STDERR")
	flag.StringVar(&cmdsFile, "cmd-file", "$HOME/.i3/i3cat.conf", "File listing of the commands to run. It will read from STDIN if - is provided")
	flag.IntVar(&header.Version, "header-version", 1, "The i3bar header version")
	flag.IntVar(&header.StopSignal, "header-stopsignal", 0, "The i3bar header stop_signal. i3cat will send this signal to the processes it manages.")
	flag.IntVar(&header.ContSignal, "header-contsignal", 0, "The i3bar header cont_signal. i3cat will send this signal to the processes it manages.")
	flag.BoolVar(&header.ClickEvents, "header-clickevents", false, "The i3bar header click_events")
	flag.Parse()
}

func main() {
	// Read and parse commands to run.
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

	// Init log output.
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0660)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	// Init where i3cat will print its output.
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

	// Resolve defaults for header signals
	sigstop := syscall.SIGSTOP
	sigcont := syscall.SIGCONT
	if header.StopSignal > 0 {
		sigstop = syscall.Signal(header.StopSignal)
	}
	if header.ContSignal > 0 {
		sigcont = syscall.Signal(header.ContSignal)
	}
	header.StopSignal = int(syscall.SIGUSR1)
	header.ContSignal = int(syscall.SIGUSR2)

	// We print the header of i3bar
	hb, err := json.Marshal(header)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(out, "%s\n[\n", hb)

	// Create the block aggregator and start the commands
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

	// Listen for worthy signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)

	for {
		// TODO handle sigcont and sigstop received from i3bar, and forward to cmds
		s := <-c
		switch s {
		case syscall.SIGTERM:
			fallthrough
		case os.Interrupt:
			// Kill all processes on interrupt
			log.Println("SIGINT or SIGTERM received: terminating all processes...")
			for _, cmdio := range ba.CmdIOs {
				if err := cmdio.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
					log.Println(err)
					if err := cmdio.Cmd.Process.Kill(); err != nil {
						log.Println(err)
					}
				}
			}
			os.Exit(0)
		case syscall.SIGUSR1:
			log.Printf("SIGUSR1 received: forwarding signal %d to all processes...\n", sigstop)
			for _, cmdio := range ba.CmdIOs {
				if err := cmdio.Cmd.Process.Signal(sigstop); err != nil {
					log.Println(err)
				}
			}
		case syscall.SIGUSR2:
			log.Printf("SIGUSR1 received: forwarding signal %d to all processes...\n", sigcont)
			for _, cmdio := range ba.CmdIOs {
				if err := cmdio.Cmd.Process.Signal(sigcont); err != nil {
					log.Println(err)
				}
			}
		}
	}
}
