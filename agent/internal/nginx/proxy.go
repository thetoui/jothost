package nginx

// proxyLocation is the location block for a site served by an application
// rather than by files.
//
// It replaces the try_files block, not the whole server: a Node.js application
// still wants nginx in front of it for TLS, for the request headers below, and
// so that the application never has to be the thing listening on port 80 as
// root.
//
// The headers are what an application behind a proxy needs to know:
//
//   - Host, because otherwise the application sees 127.0.0.1 and builds every
//     absolute URL wrong.
//   - X-Real-IP and X-Forwarded-For, because otherwise every request appears
//     to come from the proxy and rate limiting, logging, and abuse handling
//     all see one client.
//   - X-Forwarded-Proto, because that is how a framework decides whether to
//     set a Secure cookie or redirect to HTTPS. An application that gets this
//     wrong on a TLS site redirects to itself forever. It is $scheme rather
//     than a literal, so the same block is correct in both the HTTP and the
//     HTTPS server — a hard-coded "https" in the plain block would tell the
//     application a request was secure when it was not.
//
// Upgrade and Connection are passed through so WebSockets work. A Node
// application that cannot accept a WebSocket is a Node application with half
// its use missing, and the two lines that enable it are easy to leave out and
// hard to diagnose afterwards.
const proxyLocation = `
    location / {
        proxy_pass http://127.0.0.1:{{ .ProxyPort }};
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;

        # WebSocket upgrade. $connection_upgrade is defined in the panel's own
        # http block; nginx has no built-in for it.
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        # An application that has not answered in this long is not going to.
        # The read timeout is generous because a long-polling or streaming
        # endpoint is a legitimate thing to have.
        proxy_connect_timeout 5s;
        proxy_send_timeout 60s;
        proxy_read_timeout 300s;

        # Buffering off so a streamed response reaches the client as it is
        # produced rather than when it finishes.
        proxy_buffering off;
        proxy_redirect off;
    }
`

// connectionUpgradeMap defines the variable the proxy block above needs.
//
// It belongs in the http block rather than a server block, so it is written
// once into the panel's own include rather than repeated per site — nginx
// refuses to start with a duplicate map.
const ConnectionUpgradeMap = `# Managed by JotHost Panel. Manual edits are overwritten.
#
# Needed by every reverse-proxied site: nginx has no built-in variable that
# carries a WebSocket upgrade through, and defining this per server block would
# be a duplicate definition nginx refuses to start with.
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
`
