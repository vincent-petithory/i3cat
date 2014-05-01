# i3cat [![wercker status](https://app.wercker.com/status/f9749c41b63024450dc703f139e922ce/m/ "wercker status")](https://app.wercker.com/project/bykey/f9749c41b63024450dc703f139e922ce) [![Gobuild Download](http://gobuild.io/badge/github.com/vincent-petithory/i3cat/download.png)](http://gobuild.io/github.com/vincent-petithory/i3cat)

A simple program to combine multiple i3bar JSON inputs into one to forward to i3bar.

## Motivation

 * enjoy the simplicity of i3status, do not replace it with a fully featured wrapper
 * use simple shell scripts to add new i3bar blocks

## Walkthrough

### Install

Several options:

 * Download a binary for your platform [here](http://gobuild.io/github.com/vincent-petithory/i3cat)
 * [Install Go](http://golang.org/doc/install) and run `go get github.com/vincent-petithory/i3cat`
 * If you're on Arch Linux, you can install from [AUR](https://aur.archlinux.org/packages/i3cat-git/).

### Get what you had with i3status:

`status_command i3status --config ~/.i3/status` becomes `status_command echo "i3status --config ~/.i3/status" | i3cat`

But since you will want to add other blocks, it's more handy to add the commands in a conf file:

	$ cat ~/.i3/i3cat.conf
	# i3 status
	i3status -c ~/.i3/status

and the status command is now `status_command i3cat` (`~/.i3/i3cat.conf` is the default location for its conf file).

### Add a block

Say we want to display the current song played by MPD every 10 seconds. The script could be:

	$ cat ~/.i3/mpd-nowplaying.sh
	#!/bin/sh
	while :; do
	  echo '[{"name":"mpd","instance":"now playing","full_text":" '$(mpc current)'"}]'
	  sleep 10
	done

Edit `~/.i3/i3cat.conf`:

	$ cat i3cat.conf
	# mpc status
	~/.i3/mpd-nowplaying.sh
	# i3 status
	i3status -c ~/.i3/status

The order matters: the output of the commands are sent to i3bar in that order.
Lines starting with `#` are comments and ignored.

Note the JSON output of the script is an array. `i3cat` also supports variants like the output from `i3status`: a i3bar header (or not) followed by an infinite array.

### More

Run `i3cat -h` for a list of options:

    Usage of i3cat:
      -cmd-file="$HOME/.i3/i3cat.conf": File listing of the commands to run. It will read from STDIN if - is provided
      -debug-file="": Outputs JSON to this file as well; for debugging what is sent to i3bar.
      -header-clickevents=false: The i3bar header click_events
      -header-contsignal=0: The i3bar header cont_signal
      -header-stopsignal=0: The i3bar header stop_signal
      -header-version=1: The i3bar header version
      -log-file="": Logs i3cat events in this file. Defaults to STDERR

## Design

`i3cat` sends data to i3bar only when necessary: when a command sends an updated output of its blocks, `i3cat` caches it and sends to i3bar the updated output of all blocks, using the latest cached blocks of the other commands. This means commands don't need to have the same update frequency.

It is not advised to send SIGSTOP and SIGCONT signals to`i3cat`, as its subprocesses will continue to output data anyway.
For pausing and resuming processing (usually asked by i3bar), `i3cat` will listen for SIGUSR1 and SIGUSR2 for pausing and resuming, respectively. It will then forward the signals specified with `-header-stopsignal` and `-header-contsignal` flags (defaults to SIGSTOP and SIGCONT) to all its managed processes.

## TODO

 * Expose click events sent by i3bar to i3cat's STDIN
