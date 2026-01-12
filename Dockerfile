FROM golang:1.25.0 as builder

FROM builder as build
WORKDIR /tcpulse
COPY . /tcpulse
RUN make build CGO_ENABLED=0

FROM alpine as runtime
COPY --from=build /tcpulse/tcpulse ./
ENTRYPOINT ["./tcpulse"]
