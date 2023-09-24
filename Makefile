# デフォルトターゲット
all: install_alp install_pt_query_digest install_query_digester

install_alp:
	@echo "Installing alp..."
	cd ~ && \
	wget https://github.com/tkuchiki/alp/releases/download/v1.0.12/alp_linux_amd64.tar.gz && \
	tar xzf alp_linux_amd64.tar.gz && \
	sudo install alp /usr/local/bin/alp && \
	rm alp_linux_amd64.tar.gz

# library2のインストール
install_pt_query_digest:
	@echo "Installing pt_query_digest..."
	# ここにlibrary2をインストールするためのコマンドやスクリプトを記述
	sudo apt install percona-toolkit

install_query_digester:
	@echo "Installing query_digester..."
	cd ~ && \
	git clone https://github.com/kazeburo/query-digester.git && \
	cd query-digester && \
	sudo install query-digester /usr/local/bin

.PHONY: all install_alp install_pt_query_digest install_query_digester