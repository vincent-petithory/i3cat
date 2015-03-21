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
	"os/signal"
	"strings"
	"syscall"
)

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
	stdFlagSet.BoolVar(&header.ClickEvents, "header-clickevents", false, "The i3bar header click_events")

	decFlagSet := flag.NewFlagSet("decode", flag.ExitOnError)
	var decField string

	encFlagSet := flag.NewFlagSet("encode", flag.ExitOnError)
	var block Block
	var singleBlock bool
	encFlagSet.BoolVar(&singleBlock, "single", false, "If true, the block will not be in a JSON array. This allows to combine other blocks before sending to i3bar.")
	encFlagSet.StringVar(&block.ShortText, "short-text", "", "the block.short_text field to encode.")
	encFlagSet.StringVar(&block.Color, "color", "", "the block.color field to encode.")
	encFlagSet.IntVar(&block.MinWidth, "min-width", 0, "the block.min_width field to encode.")
	encFlagSet.StringVar(&block.Align, "align", "", "the block.align field to encode.")
	encFlagSet.StringVar(&block.Name, "name", "", "the block.name field to encode.")
	encFlagSet.StringVar(&block.Instance, "instance", "", "the block.instance field to encode.")
	encFlagSet.BoolVar(&block.Urgent, "urgent", false, "the block.urgent field to encode.")
	encFlagSet.BoolVar(&block.Separator, "separator", true, "the block.separator field to encode.")
	encFlagSet.IntVar(&block.SeparatorBlockWidth, "separator-block-width", 0, "the block.separator_block_width field to encode.")

	usage := func() {
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

	switch {
	case len(os.Args) > 1 && os.Args[1] == "decode":
		if err := decFlagSet.Parse(os.Args[2:]); err != nil {
			log.Print(err)
			usage()
			os.Exit(2)
		}
		if decFlagSet.NArg() == 0 {
			usage()
			os.Exit(2)
		}
		decField = decFlagSet.Arg(0)
		if err := DecodeClickEvent(os.Stdout, os.Stdin, decField); err != nil {
			log.Fatal(err)
		}
	case len(os.Args) > 1 && os.Args[1] == "encode":
		if err := encFlagSet.Parse(os.Args[2:]); err != nil {
			log.Print(err)
			usage()
			os.Exit(2)
		}
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
		if err := EncodeBlock(os.Stdout, block, singleBlock); err != nil {
			log.Fatal(err)
		}
	default:
		if err := stdFlagSet.Parse(os.Args[1:]); err != nil {
			log.Print(err)
			usage()
			os.Exit(2)
		}
		if stdFlagSet.NArg() > 0 {
			usage()
			os.Exit(2)
		}
		CatBlocksToI3Bar(cmdsFile, header, logFile, debugFile)
	}
}

func EncodeBlock(w io.Writer, block Block, single bool) error {
	var v interface{}
	if single {
		v = block
	} else {
		v = []Block{block}
	}
	return json.NewEncoder(w).Encode(v)
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
	var commands []string
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
	if err := cmdsReader.Close(); err != nil {
		log.Fatal(err)
	}

	// Init log output.
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0660)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			_ = f.Close()
		}()
		log.SetOutput(f)
	}

	// Init where i3cat will print its output.
	var out io.Writer
	if debugFile != "" {
		f, err := os.OpenFile(debugFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0660)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			_ = f.Close()
		}()
		out = io.MultiWriter(os.Stdout, f)
	} else {
		out = os.Stdout
	}

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
	var cmdios []*CmdIO
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
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	for {
		s := <-c
		switch s {
		case syscall.SIGTERM:
			fallthrough
		case os.Interrupt:
			// Kill all processes on interrupt
			log.Println("SIGINT or SIGTERM received: terminating all processes...")
			for _, cmdio := range ba.CmdIOs {
				if err := cmdio.Close(); err != nil {
					log.Println(err)
				}
			}
			os.Exit(0)
		}
	}
}
