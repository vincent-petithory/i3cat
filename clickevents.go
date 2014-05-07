package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"unicode"
)

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
