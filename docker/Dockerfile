# One Dockerfile for both binaries. The ENTRYPOINT argument is the only difference between
# the API image and the worker image, which is the packaging counterpart of what fx does in
# the code: one source tree, several compositions.
FROM golang:1.26-alpine AS build

ARG ENTRYPOINT=api
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/app ./app/cmd/${ENTRYPOINT}

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app

COPY --from=build /out/app /app

# Numeric UID, so the runtime resolves the user without depending on /etc/passwd.
USER 10001

EXPOSE 3000

ENTRYPOINT ["/app"]
