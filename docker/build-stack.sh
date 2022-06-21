#!/bin/bash

echo "Create network"
docker network create ditchnet

echo "Create database container"
docker run -d --name pg_ditchnet --network ditchnet -e POSTGRES_PASSWORD=$1 --restart unless-stopped postgres:14

echo "Build DitchNet image"
docker build -t ditchnet ditchnet

echo "Initialize database"
docker cp ../db.sql pg_ditchnet:/db.sql
docker exec -t pg_ditchnet psql -U postgres -f /db.sql
docker exec -t pg_ditchnet rm -f /db.sql

echo "Start DitchNet container"
docker run -d --name ditchnet --network ditchnet -p 5566:5566 --restart unless-stopped ditchnet