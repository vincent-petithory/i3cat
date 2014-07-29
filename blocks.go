package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"unicode"

	"github.com/vincent-petithory/structfield"
)

// Header defines the struct of the header in the i3bar protocol.
type Header struct {
	Version     int  `json:"version"`
	StopSignal  int  `json:"stop_signal,omitempty"`
	ContSignal  int  `json:"cont_signal,omitempty"`
	ClickEvents bool `json:"click_events,omitempty"`
}

var trueBoolTransformer = structfield.TransformerFunc(func(field string, value interface{}) (string, interface{}) {
	switch x := value.(type) {
	case bool:
		if !x {
			return field, false
		}
	default:
		panic("trueBoolTransformer: expected bool")
	}
	return "", nil
})

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
	Separator           bool   `json:"separator"`
	SeparatorBlockWidth int    `json:"separator_block_width,omitempty"`
}

func (b Block) MarshalJSON() ([]byte, error) {
	m := structfield.Transform(b, map[string]structfield.Transformer{
		"separator": trueBoolTransformer,
	})
	return json.Marshal(m)
}

func (b *Block) UnmarshalJSON(data []byte) error {
	type blockAlias Block
	ba := blockAlias{}
	if err := json.Unmarshal(data, &ba); err != nil {
		return err
	}
	*b = Block(ba)

	sep := struct {
		Value *bool `json:"separator"`
	}{}
	if err := json.Unmarshal(data, &sep); err != nil {
		return err
	}
	if sep.Value != nil {
		b.Separator = *sep.Value
	} else {
		// defaults to true
		b.Separator = true
	}
	return nil
}

// String implements Stringer interface.
func (b Block) String() string {
	return b.FullText
}

// BlockAggregate relates a CmdIO to the Blocks it produced during one update.
type BlockAggregate struct {
	CmdIO  *CmdIO
	Blocks []*Block
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
