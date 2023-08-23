# algia

nostr CLI client written in Go

## Usage

```
NAME:
   algia - A new cli application

USAGE:
   algia [global options] command [command options] [arguments...]

DESCRIPTION:
   A cli application for nostr

COMMANDS:
   timeline, tl  show timeline
   post, n       post new note
   reply, r      reply to the note
   repost, b     repost the note
   like, l       like the note
   delete, d     delete the note
   help, h       Shows a list of commands or help for one command

GLOBAL OPTIONS:
   -a value    profile name
   -V          verbose (default: false)
   --help, -h  show help
```

## Installation

Download binary from Release page.

Or install with go install command.
```
go install github.com/mattn/algia@latest
```

## Configuration

Minimal configuration. Need to be at ~/.config/algia/config.json

```json
{
  "relays": {
    "wss://relay-jp.nostr.wirednet.jp": {
      "read": true,
      "write": true,
      "search": false
    }
  },
  "privatekey": "nsecXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
}

```

## TODO

* [x] like
* [x] repost
* [ ] upload images

## FAQ

Do you use proxy? then set environment variable `HTTP_PROXY` like below.

    HTTP_PROXY=http://myproxy.example.com:8080

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
