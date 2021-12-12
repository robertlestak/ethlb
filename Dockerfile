FROM golang:1.17 as builder

WORKDIR /src

COPY . .

RUN go build -o chainmgr .

FROM golang:1.17 as app

WORKDIR /app

COPY --from=builder /src/chainmgr /app/chainmgr

ENTRYPOINT [ "/app/chainmgr" ]