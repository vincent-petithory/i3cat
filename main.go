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
	// writer is the underlying stream where Cmd outputs data.
	writer io.WriteCloser
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
	writer, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	cmdio := CmdIO{
		Cmd:    cmd,
		reader: reader,
		writer: writer,
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
					break IgnoreChars
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
				if err == io.EOF {
					log.Println("reached EOF")
					return
				}
				log.Printf("Invalid JSON input: all decoding methods failed (%v)\n", err)
				// consume all remaining data to prevent looping forever on a decoding err
				for r.Buffered() > 0 {
					_, err := r.ReadByte()
					if err != nil {
						log.Println(err)
					}
				}
				// send an error block
				b = []*Block{
					{
						FullText: fmt.Sprintf("Error parsing input: %v", err),
						Color:    "#FF0000",
					},
				}
			}
			blockAggregatesCh <- &BlockAggregate{c, b}
		}
	}()
	return nil
}

// Close closes reader and writers of this CmdIO.
func (c *CmdIO) Close() error {
	if err := c.reader.Close(); err != nil {
		return err
	}
	if err := c.writer.Close(); err != nil {
		return err
	}
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

// ForwardClickEvents relays click events emitted on ceCh to interested parties.
// An interested party is a cmdio whose
func (ba *BlockAggregator) ForwardClickEvents(ceCh <-chan ClickEvent) {
FWCE:
	for ce := range ceCh {
		for _, cmdio := range ba.CmdIOs {
			blocks, ok := ba.Blocks[cmdio]
			if !ok {
				continue
			}
			for _, block := range blocks {
				if block.Name == ce.Name && block.Instance == ce.Instance {
					if err := json.NewEncoder(cmdio.writer).Encode(ce); err != nil {
						log.Println(err)
					}
					log.Printf("Sending click event %+v to %s\n", ce, cmdio.Cmd.Args)
					// One of the blocks of this cmdio matched.
					// We don't want more since a name/instance is supposed to be unique.
					continue FWCE
				}
			}
		}
		log.Printf("No block source found for click event %+v\n", ce)
	}
}

// ClickEvent holds data sent by i3bar when the user clicks a block.
type ClickEvent struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Button   int    `json:"button"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
}

// ClickEventsListener parses the click event stream and notifies its subscribers.
type ClickEventsListener struct {
	r               io.Reader
	clickEventChans []chan ClickEvent
}

// NewClickEventsListener returns a ClickEventsListener which reads from r.
func NewClickEventsListener(r io.Reader) *ClickEventsListener {
	return &ClickEventsListener{r: r, clickEventChans: make([]chan ClickEvent, 0)}
}

// Listen reads and decodes the click event stream and forwards them to the channels subscribers.
func (cel *ClickEventsListener) Listen() {
	r := bufio.NewReader(cel.r)
	dec := json.NewDecoder(r)
	for {
		var ce ClickEvent
		// Ignore unwanted chars first
	IgnoreChars:
		for {
			ruune, _, err := r.ReadRune()
			if err != nil {
				log.Println(err)
				break IgnoreChars
			}
			switch {
			case unicode.IsSpace(ruune):
				// Loop again
			case ruune == '[':
				// Loop again
			case ruune == ',':
				break IgnoreChars
			default:
				r.UnreadRune()
				break IgnoreChars
			}
		}
		err := dec.Decode(&ce)
		switch {
		case err == io.EOF:
			log.Println("ClickEventsListener: reached EOF")
			return
		case err != nil:
			log.Printf("ClickEventsListener: invalid JSON input: %v\n", err)
			return
		default:
			log.Printf("Received click event %+v\n", ce)
			for _, ch := range cel.clickEventChans {
				go func() {
					ch <- ce
				}()
			}
		}
	}
}

// Notify returns a channel which will be fed by incoming ClickEvents.
func (cel *ClickEventsListener) Notify() chan ClickEvent {
	ch := make(chan ClickEvent)
	cel.clickEventChans = append(cel.clickEventChans, ch)
	return ch
}

func main() {
	var debugFile string
	var logFile string
	var cmdsFile string
	var header Header

	stdFlagSet := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	stdFlagSet.StringVar(&debugFile, "debug-file", "", "Outputs JSON to this file as well; for debugging what is sent to i3bar.")
	stdFlagSet.StringVar(&logFile, "log-file", "", "Logs i3cat events in this file. Defaults to STDERR")
	stdFlagSet.StringVar(&cmdsFile, "cmd-file", "$HOME/.i3/i3cat.conf", "File listing of the commands to run. It will read from STDIN if - is provided")
	stdFlagSet.IntVar(&header.Version, "header-version", 1, "The i3bar header version")
	stdFlagSet.IntVar(&header.StopSignal, "header-stopsignal", 0, "The i3bar header stop_signal. i3cat will send this signal to the processes it manages.")
	stdFlagSet.IntVar(&header.ContSignal, "header-contsignal", 0, "The i3bar header cont_signal. i3cat will send this signal to the processes it manages.")
	stdFlagSet.BoolVar(&header.ClickEvents, "header-clickevents", false, "The i3bar header click_events")

	decFlagSet := flag.NewFlagSet("decode", flag.ExitOnError)
	var decField string

	encFlagSet := flag.NewFlagSet("encode", flag.ExitOnError)
	var block Block
	encFlagSet.StringVar(&block.ShortText, "short-text", "", "the block.short_text field to encode.")
	encFlagSet.StringVar(&block.Color, "color", "", "the block.color field to encode.")
	encFlagSet.IntVar(&block.MinWidth, "min-width", 0, "the block.min_width field to encode.")
	encFlagSet.StringVar(&block.Align, "align", "", "the block.align field to encode.")
	encFlagSet.StringVar(&block.Name, "name", "", "the block.name field to encode.")
	encFlagSet.StringVar(&block.Instance, "instance", "", "the block.instance field to encode.")
	encFlagSet.BoolVar(&block.Urgent, "urgent", false, "the block.urgent field to encode.")
	encFlagSet.BoolVar(&block.Separator, "separator", false, "the block.separator field to encode.")
	encFlagSet.IntVar(&block.SeparatorBlockWidth, "separator-block-width", 0, "the block.separator_block_width field to encode.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: i3cat [COMMAND] [ARGS]

  If COMMAND is not specified, i3cat will print i3bar blocks to stdout.

`)
		stdFlagSet.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
decode: FIELD
  
  Reads STDIN and decodes a JSON payload representing a click event; typically sent by i3bar.
  It will print the FIELD from the JSON structure to stdout.
  
  Possible fields are name, instance, button, x, y.

`)
		decFlagSet.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
encode: [OPTS] [FULL_TEXT...]
  
  Concats FULL_TEXT arguments, separated with spaces, and encodes it as an i3bar block JSON payload.
  If FULL_TEXT is -, it will read from STDIN instead.
  
  The other fields of an i3bar block are optional and specified with the following options:

`)
		encFlagSet.PrintDefaults()
	}

	flag.Parse()
	switch {
	case flag.Arg(0) == "decode":
		decFlagSet.Parse(os.Args[2:])
		if decFlagSet.NArg() == 0 {
			flag.Usage()
			os.Exit(2)
		}
		decField = decFlagSet.Arg(0)
		if err := DecodeClickEvent(os.Stdout, os.Stdin, decField); err != nil {
			log.Fatal(err)
		}
	case flag.Arg(0) == "encode":
		encFlagSet.Parse(os.Args[2:])
		switch {
		case encFlagSet.NArg() == 0:
			fallthrough
		case encFlagSet.NArg() == 1 && encFlagSet.Arg(0) == "-":
			fullText, err := ioutil.ReadAll(os.Stdin)
			if err != nil {
				log.Fatal(err)
			}
			block.FullText = string(fullText)
		case encFlagSet.NArg() > 0:
			block.FullText = strings.Join(encFlagSet.Args(), " ")
		}
		if err := EncodeBlock(os.Stdout, block); err != nil {
			log.Fatal(err)
		}
	default:
		stdFlagSet.Parse(os.Args[1:])
		if stdFlagSet.NArg() > 0 {
			flag.Usage()
			os.Exit(2)
		}
		CatBlocksToI3Bar(cmdsFile, header, logFile, debugFile)
	}
}

func EncodeBlock(w io.Writer, block Block) error {
	return json.NewEncoder(w).Encode(block)
}

func DecodeClickEvent(w io.Writer, r io.Reader, field string) error {
	var ce ClickEvent
	if err := json.NewDecoder(r).Decode(&ce); err != nil {
		return err
	}
	var v interface{}
	switch field {
	case "name":
		v = ce.Name
	case "instance":
		v = ce.Instance
	case "button":
		v = ce.Button
	case "x":
		v = ce.X
	case "y":
		v = ce.Y
	default:
		return fmt.Errorf("unknown property %s", field)
	}
	fmt.Fprintln(w, v)
	return nil
}

func CatBlocksToI3Bar(cmdsFile string, header Header, logFile string, debugFile string) {
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

	// Listen for click events sent from i3bar
	cel := NewClickEventsListener(os.Stdin)
	go cel.Listen()

	// Create the block aggregator and start the commands
	blocksCh := make(chan *BlockAggregate)
	cmdios := make([]*CmdIO, 0)
	ba := NewBlockAggregator(out)
	for _, c := range commands {
		cmdio, err := NewCmdIO(c)
		if err != nil {
			log.Fatal(err)
		}
		cmdios = append(cmdios, cmdio)
		if err := cmdio.Start(blocksCh); err != nil {
			log.Fatal(err)
		} else {
			log.Printf("Starting command: %s", c)
		}
	}
	ba.CmdIOs = cmdios
	go ba.Aggregate(blocksCh)

	ceCh := cel.Notify()
	go ba.ForwardClickEvents(ceCh)

	// Listen for worthy signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)

	for {
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
				if err := cmdio.Close(); err != nil {
					log.Println(err)
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
