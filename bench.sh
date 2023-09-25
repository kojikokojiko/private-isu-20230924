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

# /tmp ディレクトリ内の .digest ファイルを列挙し、それをコピーする
for file in "$SOURCE_DIR"/*.digest; do
    # ファイルが存在しない、または既に目的地に同名のファイルが存在する場合、スキップする
    [ ! -e "$file" ] && continue
    dest_file="$DEST_DIR/$(basename "$file")"
    [ -e "$dest_file" ] && continue

    # ファイルをコピーする
    cp "$file" "$DEST_DIR/"
done

echo "Copying process completed!"