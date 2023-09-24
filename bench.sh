#!/bin/bash

# 1つ目のコマンドをバックグラウンドで実行
/home/isucon/private_isu/benchmarker/bin/benchmarker -u /home/isucon/private_isu/benchmarker/userdata -t http://localhost &

# 2つ目のコマンドをフォアグラウンドで実行
sudo query-digester -duration 70

# バックグラウンドのプロセスが終了するのを待つ
wait