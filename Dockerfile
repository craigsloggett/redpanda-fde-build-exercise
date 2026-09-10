FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY reason ./reason
COPY serve ./serve
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/reason ./reason \
  && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/serve ./serve

FROM gcr.io/distroless/static-debian12:nonroot AS reason
COPY --from=build /out/reason /reason
ENTRYPOINT ["/reason"]

FROM gcr.io/distroless/static-debian12:nonroot AS serve
COPY --from=build /out/serve /serve
EXPOSE 8080
ENTRYPOINT ["/serve"]
