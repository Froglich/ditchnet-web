#!/bin/bash

docker build -t ditchnet ditchnet
docker network create ditchnet
docker run -d --name pg_ditchnet --network ditchnet -e POSTGRES_PASSWORD=$1 --restart unless-stopped postgres:14
sleep 10 #give the db some time to initialize
docker cp ../db.sql pg_ditchnet:/db.sql
docker exec -t pg_ditchnet psql -U postgres -f /db.sql
docker exec -t pg_ditchnet rm -f /db.sql
docker run -d --name ditchnet --network ditchnet -p 5566:5566 --restart unless-stopped ditchnet