FROM golang:latest AS build
RUN mkdir /app 
ADD . /go/src/ImagineLearning/node-nlb-sync/ 
WORKDIR /go/src/ImagineLearning/node-nlb-sync 
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main . 

FROM golang:1.9-alpine
WORKDIR /app
COPY --from=build /go/src/ImagineLearning/node-nlb-sync .
ENTRYPOINT [ "/app/main" ]