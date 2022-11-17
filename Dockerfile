FROM devopsworks/golang-upx:1.18 AS builder

ENV GO111MODULE=on
ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /repo

# all the steps are cached
ADD go.mod .
ADD go.sum .
# if go.mod/go.sum not changed, this step is also cached
RUN go mod download

ADD . ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -o api main.go && \
strip api && /usr/local/bin/upx api

FROM alpine:3.14

COPY --from=builder /usr/share/zoneinfo/Asia/Shanghai /usr/share/zoneinfo/Asia/Shanghai
ENV TZ="Asia/Shanghai"

WORKDIR /repo

COPY --from=builder /repo/api ./

COPY .env* ./

ENTRYPOINT ["/repo/api"]
