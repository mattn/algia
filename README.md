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
   note, n       post new note
   reply, n      reply to the note
   renote, b     renote the note
   vote, v       vote the note
   help, h       Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --help, -h  show help
```

## Installation

```
go install github.com/mattn/algia@latest
```

## Configuration

Minimal configuration. Need to be at ~/.config/algia/config.json

```json
{
  "relays": {
    "wss://nostr.h3z.jp": {
      "Read": true,
      "Write": true
    }
  },
  "privatekey": "nsecXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
}

```

## TODO

* [x] like
* [x] repost
* [ ] upload images

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
