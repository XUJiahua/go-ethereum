#!/bin/bash
set -x

dir="/tmp/eth/60"
datadir="$dir/0$1"
mkdir -p $datadir

geth init --datadir="$datadir" genesis.json

read -p "Press enter to continue"

bootnodes="enode://fe0eaa382868b61a5712dcb8690b0334a446f08425288fe5c20dc4dd92b703f01eeb581f2229bee067176e6b2fa03f39c7cf2b1989d92980b0783de0561394bf@127.0.0.1:0?discport=30300"
port="3030$1"
httpport="810$1"
log="$dir/0$1.log"
geth --datadir="$datadir" --verbosity 6 \
  --ipcdisable --port $port --http --http.port $httpport \
  --bootnodes "$bootnodes" \
  console 2>> $log
