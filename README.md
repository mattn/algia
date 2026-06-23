# algia

nostr CLI client written in Go

## Usage

```
NAME:
   algia - A cli application for nostr

USAGE:
   algia [global options] command [command options] 

DESCRIPTION:
   A cli application for nostr

COMMANDS:
   timeline, tl  show timeline
   stream        show stream
   post, n       post new note
   reply, r      reply to the note
   repost, b     repost the note
   unrepost, B   unrepost the note
   like, l       like the note
   unlike, L     unlike the note
   delete, d     delete the note
   search, s     search notes
   dm            direct messages (list/timeline/post)
   bm            bookmarks (list/post)
   list          lists (show/add/remove/delete)
   channel       public chat channels (create/list/timeline/stream/post)
   blossom       Blossom media servers (upload/list/get/delete/check/mirror)
   profile       show profile
   powa          post ぽわ〜
   puru          post ぷる
   zap           zap [note|npub|nevent]
   version       show version
   help, h       Shows a list of commands or help for one command

GLOBAL OPTIONS:
   -a value        profile name
   --relays value  relays
   -V              verbose (default: false)
   --help, -h      show help
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

If you want to operate [Blossom](https://github.com/hzrd149/blossom) media servers,
add `blossom-servers`. Uploads, deletes and checks are mirrored to every listed
server; downloads try them in order. Override per-invocation with `--server`/`-s`
(repeatable).

```json
{
  "relays": {
   ...
  },
  "privatekey": "nsecXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "blossom-servers": ["https://blossom.band", "https://cdn.nostrcheck.me"]
}
```

```
algia blossom upload ./photo.png       # -> prints the blob URL(s)
algia blossom list                      # list your blobs
algia blossom get <sha256> -o out.png   # download a blob
algia blossom check <sha256>            # check existence per server
algia blossom delete <sha256>           # delete a blob
algia blossom mirror <blob-url>         # mirror a blob into your server(s) (BUD-04)

# bulk mirror every blob you own from another server into yours
algia blossom mirror --all --from https://other.blossom.server
# the source can also be a NIP-96 server (e.g. nostrcheck.me)
algia blossom mirror --all --nip96 --from https://nostrcheck.me
```

If you want to zap via Nostr Wallet Connect, please add `nwc-uri` which are provided from <https://nwc.getalby.com/apps/new?c=Algia>

```json
{
  "relays": {
   ...
  },
  "privatekey": "nsecXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "nwc-uri": "nostr+walletconnect://xxxxx"
}
```

## MCP

```json
{
    "mcpServers": {
        "algia": {
            "command": "/path/to/algia",
            "args": [
                "mcp"
            ]
        }
    }
}
```

## TODO

* [x] like
* [x] repost
* [x] zap
* [x] upload images

## FAQ

Do you use proxy? then set environment variable `HTTP_PROXY` like below.

    HTTP_PROXY=http://myproxy.example.com:8080

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
