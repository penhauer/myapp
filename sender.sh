#!/bin/bash
go run ./sender -answer-address localhost:60000 -offer-address localhost:50000 -video ./input.y4m 2>&1 | tee sender.log
