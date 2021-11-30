#!/bin/bash

echo "Number of nodes to run (1-9): "
read x

sudo docker build -t replinode .

sudo docker network create --subnet=172.30.0.0/16 replinetwork

PORT=50001

sudo docker run -d --rm --net replinetwork --ip 172.30.0.10 -p ${PORT}:8080 -e NODE_NAME=node1 replinode
for (( i = 0; i < $((x-1)); i++))
do
  sleep 2
  sudo docker run -d --rm --net replinetwork -p $((PORT + i+1)):8080 -e NODE_NAME=node$((i+2)) -e CLUSTER_ADDRESS=172.30.0.10 replinode
done
echo DONE!
