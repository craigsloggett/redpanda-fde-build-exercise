FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY reason ./reason
COPY serve ./serve
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/reason ./reason \
  && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serve ./serve

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS reason
COPY --from=build /out/reason /reason
ENTRYPOINT ["/reason"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS serve
COPY --from=build /out/serve /serve
EXPOSE 8080
ENTRYPOINT ["/serve"]
