package main

// usage is printed by --help and -h: the flags and defaults of the README flag table.
const usage = `Usage: postgres-dba-mcp [flags] [DATABASE_URI]

MCP server for one PostgreSQL database. The URI can also be set in DATABASE_URI.
Flags can come before or after the URI, as --name=value or --name value.

Flags:
  --access-mode        restricted | unrestricted (default restricted)
  --transport          stdio | streamable-http | sse (default stdio)
  --tools              comma list of schema, query, explain, stats, health, index, or all (default all)
  --max-rows           default row limit of a result, at least 1 (default 100)
  --max-rows-limit     highest max_rows a client may ask for (default 1000)
  --max-bytes          size cap of one tool result in bytes, at least 1024 (default 32768)
  --max-cell           longest cell in characters before it is shortened, at least 16 (default 200)
  --statement-timeout  server-side timeout of every statement, a Go duration above 0 (default 30s)
  --allow-function     function name or pattern to allow in restricted mode; repeatable
  --deny-function      function name or pattern to refuse in restricted mode; repeatable
  --host               HTTP transports: address to bind (default 127.0.0.1)
  --port               HTTP transports: port to listen on (default 8000)
  --allow-remote       HTTP transports: allow binding a non-loopback address (default false)
  --allowed-hosts      HTTP transports: extra Host values to accept, comma list
  --allowed-origins    HTTP transports: extra Origin values to accept, comma list
  --sse-host           alias of --host
  --sse-port           alias of --port
  --version            print the version and exit
  -h, --help           print this help and exit
`
