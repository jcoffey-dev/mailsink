# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Configuration
#
# Everything below is compiled into the binary at build time. The running
# container reads no environment variables and no config files, so what is
# set here is what runs. Rebuild the image to change it.
# ---------------------------------------------------------------------------

# Recipient domains mail is accepted for. Mail to any other domain is
# refused with 550 at RCPT TO. Comma-separated.
#   example.test     that domain only
#   *.example.test   any subdomain of example.test (not example.test itself)
#   *                any domain
ARG ALLOWED_DOMAINS="example.test,*.example.test"

# Client addresses allowed to connect, as CIDRs. Anything else gets 554 and
# is disconnected. Loopback is always allowed (for the healthcheck).
# Defaults to the private ranges; narrow this to your test network.
ARG ALLOWED_NETWORKS="10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

# Name used in the SMTP greeting and EHLO reply.
ARG SMTP_HOSTNAME="mailsink.test"

# Port to listen on inside the container.
ARG SMTP_PORT="25"

# Largest message accepted, in bytes (default 25 MiB).
ARG MAX_MESSAGE_SIZE="26214400"

# Most recipients per message.
ARG MAX_RECIPIENTS="100"

# Most simultaneous client connections.
ARG MAX_CONNECTIONS="100"

# ---------------------------------------------------------------------------

FROM golang:1.27-alpine AS build
ARG ALLOWED_DOMAINS ALLOWED_NETWORKS SMTP_HOSTNAME SMTP_PORT MAX_MESSAGE_SIZE MAX_RECIPIENTS MAX_CONNECTIONS
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /mailsink -ldflags "-s -w \
      -X 'main.allowedDomains=${ALLOWED_DOMAINS}' \
      -X 'main.allowedNetworks=${ALLOWED_NETWORKS}' \
      -X 'main.hostname=${SMTP_HOSTNAME}' \
      -X 'main.port=${SMTP_PORT}' \
      -X 'main.maxMessageSize=${MAX_MESSAGE_SIZE}' \
      -X 'main.maxRecipients=${MAX_RECIPIENTS}' \
      -X 'main.maxConnections=${MAX_CONNECTIONS}'" . \
 # Fail the build on a bad setting rather than at container start.
 && /mailsink -check

# The runtime image holds the one static binary and nothing else: no shell,
# no MTA, no mail spool, no network tools.
FROM scratch
ARG SMTP_PORT
COPY --from=build /mailsink /mailsink
USER 65534:65534
EXPOSE ${SMTP_PORT}
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/mailsink", "-healthcheck"]
ENTRYPOINT ["/mailsink"]
