package main

import (
	crand "crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	gsm "github.com/bradleypeabody/gorilla-sessions-memcache"
	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
)

var (
	db             *sqlx.DB
	store          *gsm.MemcacheStore
	memcacheClient *memcache.Client
)

const (
	postsPerPage  = 20
	ISO8601Format = "2006-01-02T15:04:05-07:00"
	UploadLimit   = 10 * 1024 * 1024 // 10mb
)

type User struct {
	ID          int       `db:"id"`
	AccountName string    `db:"account_name"`
	Passhash    string    `db:"passhash"`
	Authority   int       `db:"authority"`
	DelFlg      int       `db:"del_flg"`
	CreatedAt   time.Time `db:"created_at"`
}

type Post struct {
	ID           int       `db:"id"`
	UserID       int       `db:"user_id"`
	Imgdata      []byte    `db:"imgdata"`
	Body         string    `db:"body"`
	Mime         string    `db:"mime"`
	CreatedAt    time.Time `db:"created_at"`
	CommentCount int
	Comments     []Comment
	User         User
	CSRFToken    string
}

type Comment struct {
	ID        int       `db:"id"`
	PostID    int       `db:"post_id"`
	UserID    int       `db:"user_id"`
	Comment   string    `db:"comment"`
	CreatedAt time.Time `db:"created_at"`
	User      User
}

func init() {
	memdAddr := os.Getenv("ISUCONP_MEMCACHED_ADDRESS")
	if memdAddr == "" {
		memdAddr = "localhost:11211"
	}
	memcacheClient = memcache.New(memdAddr)
	store = gsm.NewMemcacheStore(memcacheClient, "iscogram_", []byte("sendagaya"))
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

func dbInitialize() {
	sqls := []string{
		"DELETE FROM users WHERE id > 1000",
		"DELETE FROM posts WHERE id > 10000",
		"DELETE FROM comments WHERE id > 100000",
		"UPDATE users SET del_flg = 0",
		"UPDATE users SET del_flg = 1 WHERE id % 50 = 0",
	}

	for _, sql := range sqls {
		db.Exec(sql)
	}
}

func deleteImageFiles() {
	files, err := os.ReadDir("../image")
	if err != nil {
		fmt.Println("Error reading directory:", err)
		return
	}

	for _, file := range files {
		fileName := file.Name()
		parts := strings.Split(fileName, ".")
		if len(parts) != 2 {
			continue
		}

		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			fmt.Println("Error converting string to integer:", err)
			continue
		}

		if idx > 10000 {
			err := os.Remove("../image/" + fileName)
			if err != nil {
				fmt.Println("Error deleting file:", err)
			} else {
				fmt.Println("Deleted:", fileName)
			}
		}
	}
}

func tryLogin(accountName, password string) *User {
	u := User{}
	err := db.Get(&u, "SELECT * FROM users WHERE account_name = ? AND del_flg = 0", accountName)
	if err != nil {
		return nil
	}

	if calculatePasshash(u.AccountName, password) == u.Passhash {
		return &u
	} else {
		return nil
	}
}

func validateUser(accountName, password string) bool {
	return regexp.MustCompile(`\A[0-9a-zA-Z_]{3,}\z`).MatchString(accountName) &&
		regexp.MustCompile(`\A[0-9a-zA-Z_]{6,}\z`).MatchString(password)
}

func digest(src string) string {
	// 文字列をバイト配列に変換
	data := []byte(src)

	// SHA-512ハッシュを計算
	hash := sha512.Sum512(data)

	// バイト配列を16進文字列に変換
	hashString := hex.EncodeToString(hash[:])

	return strings.ToLower(hashString)
}
func calculateSalt(accountName string) string {
	return digest(accountName)
}

func calculatePasshash(accountName, password string) string {
	return digest(password + ":" + calculateSalt(accountName))
}

func getSession(r *http.Request) *sessions.Session {
	session, _ := store.Get(r, "isuconp-go.session")

	return session
}

func getSessionUser(r *http.Request) User {
	session := getSession(r)
	uid, ok := session.Values["user_id"]
	if !ok || uid == nil {
		return User{}
	}

	u := User{}

	err := db.Get(&u, "SELECT * FROM `users` WHERE `id` = ?", uid)
	if err != nil {
		return User{}
	}

	return u
}

func getFlash(w http.ResponseWriter, r *http.Request, key string) string {
	session := getSession(r)
	value, ok := session.Values[key]

	if !ok || value == nil {
		return ""
	} else {
		delete(session.Values, key)
		session.Save(r, w)
		return value.(string)
	}
}

func makePosts(results []Post, csrfToken string, allComments bool) ([]Post, error) {
	var posts []Post

	// キャッシュから一括で取得するためのキーを準備
	keys := make([]string, 0, len(results)*2)
	for _, p := range results {
		keys = append(keys, fmt.Sprintf("post:%d:commentCount", p.ID))
		keys = append(keys, fmt.Sprintf("post:%d:comments", p.ID))
	}

	// GetMultiを使用して一括でキャッシュされたデータを取得
	items, err := memcacheClient.GetMulti(keys)
	if err != nil {
		return nil, err
	}

	for _, p := range results {
		commentCountKey := fmt.Sprintf("post:%d:commentCount", p.ID)
		if item, found := items[commentCountKey]; found {
			// キャッシュヒット
			p.CommentCount, _ = strconv.Atoi(string(item.Value))
		} else {
			// キャッシュミス
			err = db.Get(&p.CommentCount, "SELECT COUNT(*) FROM `comments` WHERE `post_id` = ?", p.ID)
			if err != nil {
				return nil, err
			}
			memcacheClient.Set(&memcache.Item{Key: commentCountKey, Value: []byte(strconv.Itoa(p.CommentCount))})
		}

		commentsKey := fmt.Sprintf("post:%d:comments", p.ID)
		if item, found := items[commentsKey]; found {
			// キャッシュヒット
			err = json.Unmarshal(item.Value, &p.Comments)
			if err != nil {
				return nil, err
			}
		} else {
			// キャッシュミス
			query := "SELECT c.comment, c.created_at, u.account_name as `user.account_name` FROM `comments` as c JOIN `users` as u ON c.user_id = u.id WHERE `post_id` = ? ORDER BY `created_at` DESC"
			if !allComments {
				query += " LIMIT 3"
			}
			err = db.Select(&p.Comments, query, p.ID)
			if err != nil {
				return nil, err
			}
			commentsBytes, err := json.Marshal(p.Comments)
			if err != nil {
				return nil, err
			}
			memcacheClient.Set(&memcache.Item{Key: commentsKey, Value: commentsBytes})
		}

		// キャッシュミスだった場合、DBから取得してコメントを逆順にする
		if _, found := items[commentsKey]; !found {
			for i, j := 0, len(p.Comments)-1; i < j; i, j = i+1, j-1 {
				p.Comments[i], p.Comments[j] = p.Comments[j], p.Comments[i]
			}
		}

		p.CSRFToken = csrfToken
		posts = append(posts, p)
	}

	return posts, nil
}

// func makePosts(results []Post, csrfToken string, allComments bool) ([]Post, error) {
// 	var posts []Post
// 	var cacheKeys []string
// 	cacheKeyMap := make(map[string]int) // Map to store post ID with its cache key

// 	// キャッシュキーのリストを作成し、マッピングを保存します。
// 	for _, p := range results {
// 		cacheKey := fmt.Sprintf("post:%d:comments", p.ID)
// 		cacheKeys = append(cacheKeys, cacheKey)
// 		cacheKeyMap[cacheKey] = p.ID
// 	}

// 	// GetMultiを使用して一括でキャッシュから値を取得します。
// 	items, err := memcacheClient.GetMulti(cacheKeys)
// 	if err != nil {
// 		return nil, err // 環境によってはnilを返さずに処理を続けることも可能
// 	}

// 	// 結果を処理します。
// 	for _, p := range results {
// 		cacheKey := fmt.Sprintf("post:%d:comments", p.ID)
// 		if item, found := items[cacheKey]; found {
// 			// キャッシュヒット
// 			err = json.Unmarshal(item.Value, &p.Comments)
// 			if err != nil {
// 				return nil, err
// 			}
// 		} else {
// 			// キャッシュミスの処理は、元の関数のロジックをそのまま使用します。
// 			err = db.Get(&p.CommentCount, "SELECT COUNT(*) AS `count` FROM `comments` WHERE `post_id` = ?", p.ID)
// 			if err != nil {
// 				return nil, err
// 			}

// 			query := "SELECT c.comment,c.created_at ,u.account_name as `user.account_name` FROM `comments` as c JOIN `users` as u ON c.user_id = u.id  WHERE `post_id` = ? ORDER BY `created_at` DESC"
// 			if !allComments {
// 				query += " LIMIT 3"
// 			}

// 			var comments []Comment
// 			err = db.Select(&comments, query, p.ID)
// 			if err != nil {
// 				return nil, err
// 			}

// 			// reverse
// 			for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
// 				comments[i], comments[j] = comments[j], comments[i]
// 			}

// 			p.Comments = comments

// 			// キャッシュへ保存
// 			serialized, err := json.Marshal(p.Comments)
// 			if err != nil {
// 				return nil, err
// 			}
// 			memcacheClient.Set(&memcache.Item{Key: cacheKey, Value: serialized})
// 		}

// 		p.CSRFToken = csrfToken
// 		posts = append(posts, p)
// 	}

// 	return posts, nil
// }

func imageURL(p Post) string {
	ext := ""
	if p.Mime == "image/jpeg" {
		ext = ".jpg"
	} else if p.Mime == "image/png" {
		ext = ".png"
	} else if p.Mime == "image/gif" {
		ext = ".gif"
	}

	return "/image/" + strconv.Itoa(p.ID) + ext
}

func isLogin(u User) bool {
	return u.ID != 0
}

func getCSRFToken(r *http.Request) string {
	session := getSession(r)
	csrfToken, ok := session.Values["csrf_token"]
	if !ok {
		return ""
	}
	return csrfToken.(string)
}

func secureRandomStr(b int) string {
	k := make([]byte, b)
	if _, err := crand.Read(k); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", k)
}

func getTemplPath(filename string) string {
	return path.Join("templates", filename)
}

func getInitialize(w http.ResponseWriter, r *http.Request) {
	dbInitialize()
	deleteImageFiles()
	w.WriteHeader(http.StatusOK)
}

var (
	loginTemplate = template.Must(template.ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("login.html")),
	)
)

func getLogin(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)

	if isLogin(me) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	loginTemplate.Execute(w, struct {
		Me    User
		Flash string
	}{me, getFlash(w, r, "notice")})
}

func postLogin(w http.ResponseWriter, r *http.Request) {
	if isLogin(getSessionUser(r)) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	u := tryLogin(r.FormValue("account_name"), r.FormValue("password"))

	if u != nil {
		session := getSession(r)
		session.Values["user_id"] = u.ID
		session.Values["csrf_token"] = secureRandomStr(16)
		session.Save(r, w)

		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		session := getSession(r)
		session.Values["notice"] = "アカウント名かパスワードが間違っています"
		session.Save(r, w)

		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

var (
	registerTemplate = template.Must(template.ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("register.html")),
	)
)

func getRegister(w http.ResponseWriter, r *http.Request) {
	if isLogin(getSessionUser(r)) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	registerTemplate.Execute(w, struct {
		Me    User
		Flash string
	}{User{}, getFlash(w, r, "notice")})
}

func postRegister(w http.ResponseWriter, r *http.Request) {
	if isLogin(getSessionUser(r)) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	accountName, password := r.FormValue("account_name"), r.FormValue("password")

	validated := validateUser(accountName, password)
	if !validated {
		session := getSession(r)
		session.Values["notice"] = "アカウント名は3文字以上、パスワードは6文字以上である必要があります"
		session.Save(r, w)

		http.Redirect(w, r, "/register", http.StatusFound)
		return
	}

	exists := 0
	// ユーザーが存在しない場合はエラーになるのでエラーチェックはしない
	db.Get(&exists, "SELECT 1 FROM users WHERE `account_name` = ?", accountName)

	if exists == 1 {
		session := getSession(r)
		session.Values["notice"] = "アカウント名がすでに使われています"
		session.Save(r, w)

		http.Redirect(w, r, "/register", http.StatusFound)
		return
	}

	query := "INSERT INTO `users` (`account_name`, `passhash`) VALUES (?,?)"
	result, err := db.Exec(query, accountName, calculatePasshash(accountName, password))
	if err != nil {
		log.Print(err)
		return
	}

	session := getSession(r)
	uid, err := result.LastInsertId()
	if err != nil {
		log.Print(err)
		return
	}
	session.Values["user_id"] = uid
	session.Values["csrf_token"] = secureRandomStr(16)
	session.Save(r, w)

	http.Redirect(w, r, "/", http.StatusFound)
}

func getLogout(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	delete(session.Values, "user_id")
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)

	http.Redirect(w, r, "/", http.StatusFound)
}

var (
	indexTemplate = template.Must(template.New("layout.html").Funcs(template.FuncMap{
		"imageURL": imageURL,
	}).ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("index.html"),
		getTemplPath("posts.html"),
		getTemplPath("post.html"),
	))
)

func getIndex(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)

	results := []Post{}

	// err := db.Select(&results, "SELECT `id`, `user_id`, `body`, `mime`, `created_at` FROM `posts` ORDER BY `created_at` DESC")

	err := db.Select(&results,
		"SELECT STRAIGHT_JOIN p.id, p.user_id, p.body, p.mime, p.created_at, "+
			"u.account_name as `user.account_name`"+
			// "u.id as `user.id`, u.account_name as `user.account_name`, u.passhash as `user.passhash`,"+
			// "u.authority as `user.authority`, u.del_flg as `user.del_flg`, u.created_at as `user.created_at`"+
			" FROM `posts` AS p JOIN `users` AS u ON (p.user_id=u.id) "+
			"WHERE u.del_flg=0 ORDER BY p.created_at DESC LIMIT ?", postsPerPage)

	if err != nil {
		log.Print(err)
		return
	}

	posts, err := makePosts(results, getCSRFToken(r), false)
	if err != nil {
		log.Print(err)
		return
	}

	indexTemplate.Execute(w, struct {
		Posts     []Post
		Me        User
		CSRFToken string
		Flash     string
	}{posts, me, getCSRFToken(r), getFlash(w, r, "notice")})
}

var (
	accountTemplate = template.Must(template.New("layout.html").Funcs(template.FuncMap{
		"imageURL": imageURL,
	}).ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("user.html"),
		getTemplPath("posts.html"),
		getTemplPath("post.html"),
	))
)

func getAccountName(w http.ResponseWriter, r *http.Request) {
	accountName := chi.URLParam(r, "accountName")
	user := User{}

	err := db.Get(&user, "SELECT * FROM `users` WHERE `account_name` = ? AND `del_flg` = 0", accountName)
	if err != nil {
		log.Print(err)
		return
	}

	if user.ID == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	results := []Post{}

	// err = db.Select(&results, "SELECT `id`, `user_id`, `body`, `mime`, `created_at` FROM `posts` WHERE `user_id` = ? ORDER BY `created_at` DESC", user.ID)

	err = db.Select(&results,
		"SELECT STRAIGHT_JOIN p.id, p.user_id, p.body, p.mime, p.created_at,"+
			"u.account_name as `user.account_name`"+
			// "u.id as `user.id`, u.account_name as `user.account_name`, u.passhash as `user.passhash`,"+
			// "u.authority as `user.authority`, u.del_flg as `user.del_flg`, u.created_at as `user.created_at`"+
			" FROM `posts` AS p JOIN `users` AS u ON (p.user_id=u.id) "+
			"WHERE p.user_id = ? AND u.del_flg=0 ORDER BY p.created_at DESC LIMIT ?", user.ID, postsPerPage)

	if err != nil {
		log.Print(err)
		return
	}

	posts, err := makePosts(results, getCSRFToken(r), false)
	if err != nil {
		log.Print(err)
		return
	}

	commentCount := 0
	err = db.Get(&commentCount, "SELECT COUNT(*) AS count FROM `comments` WHERE `user_id` = ?", user.ID)
	if err != nil {
		log.Print(err)
		return
	}

	postIDs := []int{}
	err = db.Select(&postIDs, "SELECT `id` FROM `posts` WHERE `user_id` = ?", user.ID)
	if err != nil {
		log.Print(err)
		return
	}
	postCount := len(postIDs)

	commentedCount := 0
	if postCount > 0 {
		s := []string{}
		for range postIDs {
			s = append(s, "?")
		}
		placeholder := strings.Join(s, ", ")

		// convert []int -> []interface{}
		args := make([]interface{}, len(postIDs))
		for i, v := range postIDs {
			args[i] = v
		}

		err = db.Get(&commentedCount, "SELECT COUNT(*) AS count FROM `comments` WHERE `post_id` IN ("+placeholder+")", args...)
		if err != nil {
			log.Print(err)
			return
		}
	}

	me := getSessionUser(r)

	accountTemplate.Execute(w, struct {
		Posts          []Post
		User           User
		PostCount      int
		CommentCount   int
		CommentedCount int
		Me             User
	}{posts, user, postCount, commentCount, commentedCount, me})
}

var (
	postsTemplate = template.Must(template.New("posts.html").Funcs(template.FuncMap{
		"imageURL": imageURL,
	}).ParseFiles(
		getTemplPath("posts.html"),
		getTemplPath("post.html"),
	))
)

func getPosts(w http.ResponseWriter, r *http.Request) {
	m, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Print(err)
		return
	}
	maxCreatedAt := m.Get("max_created_at")
	if maxCreatedAt == "" {
		return
	}

	t, err := time.Parse(ISO8601Format, maxCreatedAt)
	if err != nil {
		log.Print(err)
		return
	}

	results := []Post{}
	// err = db.Select(&results, "SELECT `id`, `user_id`, `body`, `mime`, `created_at` FROM `posts` WHERE `created_at` <= ? ORDER BY `created_at` DESC", t.Format(ISO8601Format))

	err = db.Select(&results,
		"SELECT STRAIGHT_JOIN p.id, p.user_id, p.body, p.mime, p.created_at,"+
			"u.account_name as `user.account_name`"+
			// "u.id as `user.id`, u.account_name as `user.account_name`, u.passhash as `user.passhash`,"+
			// "u.authority as `user.authority`, u.del_flg as `user.del_flg`, u.created_at as `user.created_at`"+
			" FROM `posts` AS p JOIN `users` AS u ON (p.user_id=u.id) "+
			"WHERE p.created_at <= ? AND u.del_flg=0 ORDER BY p.created_at DESC LIMIT ?", t.Format(ISO8601Format), postsPerPage)

	if err != nil {
		log.Print(err)
		return
	}

	posts, err := makePosts(results, getCSRFToken(r), false)
	if err != nil {
		log.Print(err)
		return
	}

	if len(posts) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	postsTemplate.Execute(w, posts)
}

var (
	postsIdTemplate = template.Must(template.New("layout.html").Funcs(template.FuncMap{
		"imageURL": imageURL,
	}).ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("post_id.html"),
		getTemplPath("post.html"),
	))
)

func getPostsID(w http.ResponseWriter, r *http.Request) {
	pidStr := chi.URLParam(r, "id")
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	results := []Post{}
	err = db.Select(&results,
		"SELECT STRAIGHT_JOIN p.id, p.user_id, p.body, p.mime, p.created_at,"+
			"u.account_name as `user.account_name`"+
			// "u.id as `user.id`, u.account_name as `user.account_name`, u.passhash as `user.passhash`,"+
			// "u.authority as `user.authority`, u.del_flg as `user.del_flg`, u.created_at as `user.created_at`"+
			" FROM `posts` AS p JOIN `users` AS u ON (p.user_id=u.id) "+
			"WHERE p.id= ? AND u.del_flg=0 ORDER BY p.created_at DESC LIMIT ?", pid, postsPerPage)
	if err != nil {
		log.Print(err)
		return
	}

	posts, err := makePosts(results, getCSRFToken(r), true)
	if err != nil {
		log.Print(err)
		return
	}

	if len(posts) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	p := posts[0]

	me := getSessionUser(r)

	postsIdTemplate.Execute(w, struct {
		Post Post
		Me   User
	}{p, me})
}

func postIndex(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)
	if !isLogin(me) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	if r.FormValue("csrf_token") != getCSRFToken(r) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		session := getSession(r)
		session.Values["notice"] = "画像が必須です"
		session.Save(r, w)

		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	mime := ""
	if file != nil {
		// 投稿のContent-Typeからファイルのタイプを決定する
		contentType := header.Header["Content-Type"][0]
		if strings.Contains(contentType, "jpeg") {
			mime = "image/jpeg"
		} else if strings.Contains(contentType, "png") {
			mime = "image/png"
		} else if strings.Contains(contentType, "gif") {
			mime = "image/gif"
		} else {
			session := getSession(r)
			session.Values["notice"] = "投稿できる画像形式はjpgとpngとgifだけです"
			session.Save(r, w)

			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}

	filedata, err := io.ReadAll(file)
	if err != nil {
		log.Print(err)
		return
	}

	if len(filedata) > UploadLimit {
		session := getSession(r)
		session.Values["notice"] = "ファイルサイズが大きすぎます"
		session.Save(r, w)

		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	query := "INSERT INTO `posts` (`user_id`, `mime`, `imgdata`, `body`) VALUES (?,?,?,?)"
	result, err := db.Exec(
		query,
		me.ID,
		mime,
		[]byte{},
		// filedata,
		r.FormValue("body"),
	)
	if err != nil {
		log.Print(err)
		return
	}

	pid, err := result.LastInsertId()
	if err != nil {
		log.Print(err)
		return
	}

	filename := fmt.Sprintf("../image/%d.%s", pid, getExtension(mime))

	// ファイルのディレクトリ部分を取得
	dir := filepath.Dir(filename)

	// ディレクトリが存在しない場合、ディレクトリを作成
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			// ここでエラー処理を行います。例: log.Fatal(err)
		}
	}

	err = os.WriteFile(filename, filedata, 0644)
	if err != nil {
		log.Print("Could not write file: ", err)
		return
	}

	http.Redirect(w, r, "/posts/"+strconv.FormatInt(pid, 10), http.StatusFound)
}

func getExtension(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func getImage(w http.ResponseWriter, r *http.Request) {
	pidStr := chi.URLParam(r, "id")
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	post := Post{}
	err = db.Get(&post, "SELECT * FROM `posts` WHERE `id` = ?", pid)
	if err != nil {
		log.Print(err)
		return
	}

	ext := chi.URLParam(r, "ext")

	if ext == "jpg" && post.Mime == "image/jpeg" ||
		ext == "png" && post.Mime == "image/png" ||
		ext == "gif" && post.Mime == "image/gif" {
		w.Header().Set("Content-Type", post.Mime)
		_, err := w.Write(post.Imgdata)
		if err != nil {
			log.Print(err)
			return
		}
		// // ファイルに書き出す
		filename := "../image/" + pidStr + "." + ext
		err = os.WriteFile(filename, post.Imgdata, 0666)
		os.Chmod(filename, 0666)
		if err != nil {
			log.Print(err)
			return
		}
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func postComment(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)
	if !isLogin(me) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	if r.FormValue("csrf_token") != getCSRFToken(r) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	postID, err := strconv.Atoi(r.FormValue("post_id"))
	if err != nil {
		log.Print("post_idは整数のみです")
		return
	}

	query := "INSERT INTO `comments` (`post_id`, `user_id`, `comment`) VALUES (?,?,?)"
	_, err = db.Exec(query, postID, me.ID, r.FormValue("comment"))
	if err != nil {
		log.Print(err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/posts/%d", postID), http.StatusFound)
}

var (
	adminBannnedtTemplate = template.Must(template.ParseFiles(
		getTemplPath("layout.html"),
		getTemplPath("banned.html")),
	)
)

func getAdminBanned(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)
	if !isLogin(me) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	if me.Authority == 0 {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	users := []User{}
	err := db.Select(&users, "SELECT * FROM `users` WHERE `authority` = 0 AND `del_flg` = 0 ORDER BY `created_at` DESC")
	if err != nil {
		log.Print(err)
		return
	}

	adminBannnedtTemplate.Execute(w, struct {
		Users     []User
		Me        User
		CSRFToken string
	}{users, me, getCSRFToken(r)})
}

func postAdminBanned(w http.ResponseWriter, r *http.Request) {
	me := getSessionUser(r)
	if !isLogin(me) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	if me.Authority == 0 {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if r.FormValue("csrf_token") != getCSRFToken(r) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	query := "UPDATE `users` SET `del_flg` = ? WHERE `id` = ?"

	err := r.ParseForm()
	if err != nil {
		log.Print(err)
		return
	}

	for _, id := range r.Form["uid[]"] {
		db.Exec(query, 1, id)
	}

	http.Redirect(w, r, "/admin/banned", http.StatusFound)
}

func main() {
	host := os.Getenv("ISUCONP_DB_HOST")
	if host == "" {
		// host = "localhost"
		host = "172.31.17.212"
	}
	port := os.Getenv("ISUCONP_DB_PORT")
	if port == "" {
		port = "3306"
	}
	_, err := strconv.Atoi(port)
	if err != nil {
		log.Fatalf("Failed to read DB port number from an environment variable ISUCONP_DB_PORT.\nError: %s", err.Error())
	}
	user := os.Getenv("ISUCONP_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUCONP_DB_PASSWORD")
	dbname := os.Getenv("ISUCONP_DB_NAME")
	if dbname == "" {
		dbname = "isuconp"
	}

	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&interpolateParams=true",
		user,
		password,
		host,
		port,
		dbname,
	)

	db, err = sqlx.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}
	defer db.Close()

	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)

	r := chi.NewRouter()

	r.Get("/initialize", getInitialize)
	r.Get("/login", getLogin)
	r.Post("/login", postLogin)
	r.Get("/register", getRegister)
	r.Post("/register", postRegister)
	r.Get("/logout", getLogout)
	r.Get("/", getIndex)
	r.Get("/posts", getPosts)
	r.Get("/posts/{id}", getPostsID)
	r.Post("/", postIndex)
	r.Get("/image/{id}.{ext}", getImage)
	r.Post("/comment", postComment)
	r.Get("/admin/banned", getAdminBanned)
	r.Post("/admin/banned", postAdminBanned)
	r.Get(`/@{accountName:[a-zA-Z]+}`, getAccountName)
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir("../public")).ServeHTTP(w, r)
	})

	log.Fatal(http.ListenAndServe(":8080", r))
}
