#!/bin/bash

export PION_LOG_INFO=sender_interceptor,receiver_interceptor,twcc_sender_interceptor
go run ./sender -answer-address localhost:60000 -offer-address localhost:50000 -video ../video_generator/video_files/4.y4m 2>&1 | tee sender.log
