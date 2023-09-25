#!/bin/bash

# 1つ目のコマンドをバックグラウンドで実行
/home/isucon/private_isu/benchmarker/bin/benchmarker -u /home/isucon/private_isu/benchmarker/userdata -t http://localhost &

# 2つ目のコマンドをフォアグラウンドで実行
sudo query-digester -duration 70

# バックグラウンドのプロセスが終了するのを待つ
wait




# .digestファイルをwebapp下まで持ってくる
echo "Copying process start!"
SOURCE_DIR="/tmp"
DEST_DIR="/home/isucon/private_isu/webapp/digest-log"

# DEST_DIR が存在しなければ作成
if [ ! -d "$DEST_DIR" ]; then
    mkdir -p "$DEST_DIR"
fi

# 最新の .digest ファイルを見つける
latest_file=$(ls -t "$SOURCE_DIR"/*.digest | head -n 1)

# 最新のファイルが存在するか確認する
[ ! -e "$latest_file" ] && exit 1  # ファイルが存在しない場合、スクリプトを終了

# 目的地に同名のファイルが存在するか確認する
dest_file="$DEST_DIR/$(basename "$latest_file")"
[ -e "$dest_file" ] && exit 1  # 同名のファイルが存在する場合、スクリプトを終了

# ファイルをコピーする
cp "$latest_file" "$DEST_DIR/"

echo "Copying .digest completed!"