# algia

nostr CLI client written in Go

## Usage

```
NAME:
   algia - A cli application for nostr

USAGE:
   algia [global options] command [command options] [arguments...]

COMMANDS:
   note, n       post new note
   timeline, tl  show timeline
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

* [x] vote
* [x] boost
* [ ] upload images

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
