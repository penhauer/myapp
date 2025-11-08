#!/bin/bash
go run ./receiver -answer-address 127.0.0.1:60000 -offer-address 127.0.0.1:50000 2>&1  | tee receiver.log
