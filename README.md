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
   file          Blossom/NIP-96 media servers (upload/list/get/delete/check/mirror)
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

If you want to operate media servers ([Blossom](https://github.com/hzrd149/blossom)
or [NIP-96](https://github.com/nostr-protocol/nips/blob/master/96.md)), add
`file-servers`. Uploads, deletes and checks are applied to every listed server;
downloads try them in order. Override per-invocation with `--server`/`-s`
(repeatable).

Each entry is either a bare URL (treated as Blossom) or an object with an
explicit `type` of `"blossom"` or `"nip96"`. A URL may also carry a `nip96+` /
`blossom+` prefix to pick the protocol inline (handy for `--server`).

```json
{
  "relays": {
   ...
  },
  "privatekey": "nsecXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "file-servers": [
    "https://blossom.band",
    { "url": "https://nostrcheck.me", "type": "nip96" }
  ]
}
```

```
algia file upload ./photo.png        # -> prints the blob URL(s)
algia file list                       # list your blobs
algia file get <sha256> -o out.png    # download a blob
algia file check <sha256>             # check existence per server
algia file delete <sha256>            # delete a blob
algia file mirror <blob-url>          # mirror a blob into your server(s)

# upload to a specific NIP-96 server, overriding config
algia file upload -s nip96+https://nostrcheck.me ./photo.png

# mirror only the blobs missing on the destination from another server (diff)
algia file mirror --from https://other.blossom.server https://your.blossom.server
# mirror every blob you own, regardless of what is already there
algia file mirror --all --from https://other.blossom.server https://your.blossom.server
# the destination may also come from -s/config instead of a positional arg
algia file mirror --from https://other.blossom.server
# the source can also be a NIP-96 server (e.g. nostrcheck.me)
algia file mirror --nip96 --from https://nostrcheck.me
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
