FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . . 
RUN CGO_ENABLED=0 go build -o /navsync .

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=build /navsync /navsync
ENTRYPOINT [ "/navsync" ]
