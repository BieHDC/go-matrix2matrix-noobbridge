# Matrix 2 Matrix Bridge - The easy way

This is a very simplistic implemenetation of a Matrix to Matrix Room bridge for networks that can not talk to each other, like Dendrite Pinecone and Monolith or Synapse servers.
The correct thing would be to make a proper double puppeting bridge!

## How it works
It simply logs into multiple homeservers and spreads the messages between all rooms that share the same "name".
We strip out some metadata from events to keep it simple, since this is meant to be a simple bridge in the first place.
However it supports briding media files too by downloading it from the source server and uploading it to every bridged server.
But you can enable and disable this to your liking.
Give `config-example.yaml` a read for this.

## Getting started
Copy `config-example.yaml` to `config.yaml` and add your homeservers and rooms. It will attempt to join any room you tell it to bridge but its not a member of. After that is done, just run it like `./biehdc.noobbridge -config config.yaml`. Any issues will be printed to your terminal.

## Why?
My current usecase is to bridge normal matrix servers with pinecone ones.
However this might have some other usecases at some point.