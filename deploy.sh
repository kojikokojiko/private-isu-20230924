#!/bin/bash -eux

# 設定ファイルのコピー



# 実行ファイルのビルド
cd /home/isucon/webapp/go
go build -o isucondition

# アプリ・ミドルウェアの再起動
sudo systemctl restart nginx
sudo systemctl restart mariadb
sudo systemctl restart isucondition.go

# slow query logを有効化する
QUERY="
 set global slow_query_log_file = '/var/log/mysql/mysql-slow.log';
 set global long_query_time = 0;
 set global slow_query_log = ON;
"
echo $QUERY | sudo mysql -uroot

# log permission
sudo chmod 777 /var/log/nginx /var/log/nginx/*
sudo chmod 777 /var/log/mysql /var/log/mysql/*