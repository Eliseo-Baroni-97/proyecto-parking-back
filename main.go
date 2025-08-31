package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var db *sql.DB

// ----------- CONFIG/DB -------------
func getDSN() string {
	if v := os.Getenv("MYSQL_URL"); v != "" {
		return v
	}
	return "root:tDXPIyOImvUcSPoZIpIEQwkkqpmabXMp@tcp(trolley.proxy.rlwy.net:31348)/railway?parseTime=true&charset=utf8mb4"
}

func conectarDB() {
	var err error
	db, err = sql.Open("mysql", getDSN())
	if err != nil {
		log.Fatal(err)
	}
	if err = db.Ping(); err != nil {
		log.Fatal(err)
	}
}

// ----------- TIPOS -------------
type DiaAtencion struct {
	Dia   string `json:"dia"`
	Desde string `json:"desde"`
	Hasta string `json:"hasta"`
}

type EstacionamientoNuevo struct {
	DuenioID      int           `json:"duenio_id"`
	Nombre        string        `json:"nombre"`
	Cantidad      int           `json:"cantidad"`
	Latitud       float64       `json:"latitud"`
	Longitud      float64       `json:"longitud"`
	PrecioPorHora *float64      `json:"precio_por_hora"`
	Techado       *string       `json:"techado"`
	Seguridad     []string      `json:"seguridad"`
	Banos         *bool         `json:"banos"`
	AlturaMaxM    *float64      `json:"altura_max_m"`
	Dias          []DiaAtencion `json:"dias"`
}

type ActualizacionLugar struct {
	EstacionamientoID int    `json:"estacionamiento_id"`
	DuenioID          int    `json:"duenio_id"`
	Cantidad          int    `json:"cantidad"`
	Estado            string `json:"estado"`
}

type EstadoLugar struct {
	EstacionamientoID int  `json:"estacionamiento_id"`
	Numero            int  `json:"numero"`
	Ocupado           bool `json:"ocupado"`
}

type LugarSimple struct {
	Numero  int  `json:"numero"`
	Ocupado bool `json:"ocupado"`
}

// ==== AUTH TYPES ====
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type User struct {
	ID           int
	Email        string
	PasswordHash string
	Vip          bool
}

// ----------- HELPERS -------------
func dbErr(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// ========= MIDDLEWARE (leer user_id desde el JWT) =========
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := strings.TrimSpace(c.GetHeader("Authorization"))
		if auth == "" || !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token no enviado"})
			return
		}
		tokenString := strings.TrimSpace(auth[len("Bearer "):])

		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			log.Println("❌ JWT_SECRET no configurado")
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "JWT no configurado"})
			return
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("algoritmo inválido")
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token inválido"})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Claims inválidos"})
			return
		}

		// exp
		if exp, ok := claims["exp"].(float64); ok {
			if time.Now().Unix() > int64(exp) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token expirado"})
				return
			}
		}

		// user_id o sub
		var uid int
		switch v := claims["user_id"].(type) {
		case float64:
			uid = int(v)
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				uid = n
			}
		}
		if uid == 0 {
			switch v := claims["sub"].(type) {
			case float64:
				uid = int(v)
			case string:
				if n, err := strconv.Atoi(v); err == nil {
					uid = n
				}
			}
		}
		if uid == 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Falta user_id/sub en token"})
			return
		}

		c.Set("userID", uid)
		c.Next()
	}
}

func ownsEstacionamiento(estID, userID int) bool {
	var n int
	err := db.QueryRow(`SELECT COUNT(1) FROM estacionamientos WHERE id=? AND duenio_id=?`, estID, userID).Scan(&n)
	return err == nil && n > 0
}

// —— VIP & Reservas helpers ——
func userIsVIP(userID int) (bool, error) {
	var vip int
	err := db.QueryRow(`SELECT vip FROM usuarios WHERE id=?`, userID).Scan(&vip)
	return vip == 1, err
}

func hasActiveReservation(userID, estID int) (bool, error) {
	var cnt int
	err := db.QueryRow(`
		SELECT COUNT(1)
		FROM reservas
		WHERE user_id=? AND estacionamiento_id=? AND status=1
	`, userID, estID).Scan(&cnt)
	return cnt > 0, err
}

// ----------- MAIN ------------
func main() {
	conectarDB()
	defer db.Close()
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// CORS simple
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(200)
			return
		}
		c.Next()
	})

	// ============= 🔐 AUTH ==============
	r.POST("/register", func(c *gin.Context) {
		var payload RegisterRequest
		fmt.Println("👉 Se llamó a /register")

		if err := c.BindJSON(&payload); err != nil {
			fmt.Println("❌ Error al parsear JSON:", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}
		fmt.Println("📥 Payload recibido:", payload)

		hash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Println("❌ Error al generar hash:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}

		res, err := db.Exec(
			`INSERT INTO usuarios (email, password_hash) VALUES (?,?)`,
			payload.Email, string(hash),
		)
		if err != nil {
			fmt.Println("❌ Error al insertar en DB:", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Email ya registrado"})
			return
		}

		id, _ := res.LastInsertId()
		fmt.Println("✅ Usuario registrado con ID:", id)
		c.JSON(http.StatusOK, gin.H{"id": id, "email": payload.Email})
	})

	r.POST("/login", func(c *gin.Context) {
		fmt.Println("🛠 JWT_SECRET en runtime (Railway):", os.Getenv("JWT_SECRET"))
		fmt.Println("👉 Se llamó a /login")

		var payload LoginRequest
		if err := c.BindJSON(&payload); err != nil {
			fmt.Println("❌ Error al parsear JSON:", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}
		fmt.Println("📥 Payload login recibido:", payload)

		var u User
		var vipInt int
		err := db.QueryRow(`SELECT id, email, password_hash, vip FROM usuarios WHERE email = ?`, payload.Email).
			Scan(&u.ID, &u.Email, &u.PasswordHash, &vipInt)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Credenciales inválidas"})
			return
		}
		u.Vip = vipInt == 1

		fmt.Println("🔎 Usuario encontrado:", u.Email, "hash:", u.PasswordHash)

		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(payload.Password)); err != nil {
			fmt.Println("❌ La contraseña no coincide:", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Credenciales inválidas"})
			return
		}

		fmt.Println("✅ Password correcta, generando token...")
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			fmt.Println("❌ JWT_SECRET no definido")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "JWT no configurado"})
			return
		}

		claims := jwt.MapClaims{
			"user_id": u.ID,
			"email":   u.Email,
			"vip":     u.Vip,
			"exp":     time.Now().Add(24 * time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString([]byte(secret))
		if err != nil {
			fmt.Println("❌ Error al firmar token:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Token error"})
			return
		}

		fmt.Println("✅ Login exitoso, token:", signed)
		c.JSON(http.StatusOK, gin.H{
			"token":   signed,
			"user_id": u.ID,
			"vip":     u.Vip,
		})

	})

	// ============= 🚗 ESTACIONAMIENTOS ==============
	// Crear estacionamiento (protegido)
	r.POST("/estacionamientos", AuthMiddleware(), func(c *gin.Context) {
		var in EstacionamientoNuevo
		if err := c.BindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, exists := c.Get("userID")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Sin usuario"})
			return
		}
		duenioID, ok := uidVal.(int)
		if !ok || duenioID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "userID inválido"})
			return
		}

		// Normalizar seguridad
		seg := ""
		if len(in.Seguridad) > 0 {
			allowed := map[string]bool{"camaras": true, "vigilante": true}
			out := make([]string, 0, len(in.Seguridad))
			for _, v := range in.Seguridad {
				if allowed[v] {
					out = append(out, v)
				}
			}
			if len(out) > 0 {
				seg = strings.Join(out, ",")
			}
		}

		// Normalizar baños
		banos := 0
		if in.Banos != nil && *in.Banos {
			banos = 1
		}

		// Insert principal
		res, err := db.Exec(`
			INSERT INTO estacionamientos
			  (duenio_id, nombre, cantidad, latitud, longitud,
			   precio_por_hora, techado, seguridad, banos, altura_max_m)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			duenioID, in.Nombre, in.Cantidad, in.Latitud, in.Longitud,
			in.PrecioPorHora, in.Techado, seg, banos, in.AlturaMaxM,
		)
		if err != nil {
			dbErr(c, err)
			return
		}
		nuevoID, _ := res.LastInsertId()

		// Insert días (si vienen)
		for _, dia := range in.Dias {
			_, _ = db.Exec(
				`INSERT INTO dias_atencion (estacionamiento_id, dia, desde, hasta)
         VALUES (?, ?, ?, ?)`,
				nuevoID, dia.Dia, dia.Desde, dia.Hasta,
			)
		}

		c.JSON(http.StatusCreated, gin.H{"id": nuevoID})
	})

	// Listar mis estacionamientos (protegido)
	r.GET("/mis-estacionamientos", AuthMiddleware(), func(c *gin.Context) {
		uidVal, exists := c.Get("userID")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Sin usuario"})
			return
		}
		userID := uidVal.(int)

		rows, err := db.Query(`
			SELECT id, nombre, cantidad, latitud, longitud
			FROM estacionamientos
			WHERE duenio_id = ?`, userID)
		if err != nil {
			dbErr(c, err)
			return
		}
		defer rows.Close()

		type Item struct {
			ID       int     `json:"id"`
			Nombre   string  `json:"nombre"`
			Cantidad int     `json:"cantidad"`
			Latitud  float64 `json:"latitud"`
			Longitud float64 `json:"longitud"`
		}

		var list []Item
		for rows.Next() {
			var it Item
			if err := rows.Scan(&it.ID, &it.Nombre, &it.Cantidad, &it.Latitud, &it.Longitud); err == nil {
				list = append(list, it)
			}
		}

		c.JSON(http.StatusOK, gin.H{"estacionamientos": list})
	})

	// Crear/Actualizar lugares (protegido + check de dueño)
	r.POST("/lugares", AuthMiddleware(), func(c *gin.Context) {
		var req ActualizacionLugar
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, _ := c.Get("userID")
		userID := uidVal.(int)

		if !ownsEstacionamiento(req.EstacionamientoID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "No sos dueño del estacionamiento"})
			return
		}
	})

	// Crear/Actualizar lugares (protegido + check de dueño)
	r.POST("/lugares", AuthMiddleware(), func(c *gin.Context) {
		var req ActualizacionLugar
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, _ := c.Get("userID")
		userID := uidVal.(int)

		if !ownsEstacionamiento(req.EstacionamientoID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "No sos dueño del estacionamiento"})
			return
		}

		for i := 1; i <= req.Cantidad; i++ {
			_, _ = db.Exec(`
        INSERT INTO lugares (estacionamiento_id, numero, ocupado)
        VALUES (?, ?, ?)
        ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)`,
				req.EstacionamientoID, i, false,
			)
		}
		c.JSON(http.StatusOK, gin.H{"mensaje": "OK"})
	})

	// Cambiar estado de un lugar (protegido + check de dueño)
	r.POST("/lugares/estado", AuthMiddleware(), func(c *gin.Context) {
		var in EstadoLugar
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, ok := c.Get("userID")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Sin usuario"})
			return
		}
		userID, ok := uidVal.(int)
		if !ok || userID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "userID inválido"})
			return
		}

		if !ownsEstacionamiento(in.EstacionamientoID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "No sos dueño del estacionamiento"})
			return
		}

		if _, err := db.Exec(
			`UPDATE lugares SET ocupado=? WHERE estacionamiento_id=? AND numero=?`,
			in.Ocupado, in.EstacionamientoID, in.Numero,
		); err != nil {
			dbErr(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"mensaje": "OK"})
	})

	// Estado de lugares (público)
	r.GET("/estado/:id", func(c *gin.Context) {
		id := c.Param("id")
		rows, err := db.Query(`
			SELECT numero, ocupado FROM lugares WHERE estacionamiento_id=?`, id)
		if err != nil {
			dbErr(c, err)
			return
		}
		defer rows.Close()

		var lug []LugarSimple
		for rows.Next() {
			var l LugarSimple
			if err := rows.Scan(&l.Numero, &l.Ocupado); err == nil {
				lug = append(lug, l)
			}
		}
		c.JSON(http.StatusOK, gin.H{"lugares": lug})
	})

	// Lista pública de estacionamientos (mapa)
	r.GET("/estacionamientos", func(c *gin.Context) {
		type Item struct {
			ID       int64   `json:"id"`
			Nombre   string  `json:"nombre"`
			Latitud  float64 `json:"latitud"`
			Longitud float64 `json:"longitud"`
			Total    int     `json:"total"`
			Ocupados int     `json:"ocupados"`
			Libres   int     `json:"libres"`
		}
		rows, err := db.Query(`
			SELECT e.id, e.nombre, e.latitud, e.longitud,
			       e.cantidad, COALESCE(SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END),0)
			FROM estacionamientos e
			LEFT JOIN lugares l ON l.estacionamiento_id = e.id
			GROUP BY e.id`)
		if err != nil {
			dbErr(c, err)
			return
		}
		defer rows.Close()

		var list []Item
		for rows.Next() {
			var it Item
			if err := rows.Scan(&it.ID, &it.Nombre, &it.Latitud, &it.Longitud, &it.Total, &it.Ocupados); err == nil {
				it.Libres = it.Total - it.Ocupados
				list = append(list, it)
			}
		}
		c.JSON(http.StatusOK, gin.H{"estacionamientos": list})
	})

	// ✅ Puerto dinámico
	r.GET("/public/estacionamientos/:id/detalle", func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id inválido"})
			return
		}

		// 1) Datos del estacionamiento
		var (
			eID       int
			nombre    string
			lat       float64
			lng       float64
			cantidad  int
			precio    sql.NullFloat64
			techado   sql.NullString
			seguridad sql.NullString
			banosInt  int
			altura    sql.NullFloat64
		)
		err = db.QueryRow(`
		SELECT id, nombre, latitud, longitud, cantidad,
		       precio_por_hora, techado, seguridad, IFNULL(banos,0) AS banos, altura_max_m
		FROM estacionamientos
		WHERE id = ?`,
			id,
		).Scan(&eID, &nombre, &lat, &lng, &cantidad, &precio, &techado, &seguridad, &banosInt, &altura)
		if err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "Estacionamiento no encontrado"})
				return
			}
			dbErr(c, err)
			return
		}

		// 2) Resumen (total desde e.cantidad + ocupados reales en lugares)
		var total, ocupados int
		if err := db.QueryRow(`
		SELECT e.cantidad AS total,
		       COALESCE(SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END), 0) AS ocupados
		FROM estacionamientos e
		LEFT JOIN lugares l ON l.estacionamiento_id = e.id
		WHERE e.id = ?
		GROUP BY e.id
	`, id).Scan(&total, &ocupados); err != nil {
			dbErr(c, err)
			return
		}
		libres := total - ocupados

		// 3) Días (si existen)
		rowsDias, err := db.Query(`
		SELECT dia, desde, hasta
		FROM dias_atencion
		WHERE estacionamiento_id = ?
		ORDER BY dia, desde`,
			id,
		)
		dias := make([]gin.H, 0, 7)
		if err == nil {
			defer rowsDias.Close()
			for rowsDias.Next() {
				var dia, desde, hasta string
				if err := rowsDias.Scan(&dia, &desde, &hasta); err == nil {
					dias = append(dias, gin.H{"dia": dia, "desde": desde, "hasta": hasta})
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"id":       eID,
			"nombre":   nombre,
			"latitud":  lat,
			"longitud": lng,
			"precio": func() *float64 {
				if precio.Valid {
					return &precio.Float64
				}
				return nil
			}(),
			"techado": func() *string {
				if techado.Valid {
					return &techado.String
				}
				return nil
			}(),
			"seguridad": func() *string {
				if seguridad.Valid {
					return &seguridad.String
				}
				return nil
			}(),
			"banos": banosInt == 1,
			"altura_max_m": func() *float64 {
				if altura.Valid {
					return &altura.Float64
				}
				return nil
			}(),
			"resumen": gin.H{
				"total": total, "ocupados": ocupados, "libres": libres,
			},
			"dias": dias,
		})
	})

	// GET /public/estacionamientos/:id/resumen
	// Solo total/ocupados/libres (público)
	r.GET("/public/estacionamientos/:id/resumen", func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id inválido"})
			return
		}

		var total, ocupados int
		if err := db.QueryRow(`
		SELECT e.cantidad AS total,
		       COALESCE(SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END), 0) AS ocupados
		FROM estacionamientos e
		LEFT JOIN lugares l ON l.estacionamiento_id = e.id
		WHERE e.id = ?
		GROUP BY e.id
	`, id).Scan(&total, &ocupados); err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "Estacionamiento no encontrado"})
				return
			}
			dbErr(c, err)
			return
		}

		libres := total - ocupados
		c.JSON(http.StatusOK, gin.H{"total": total, "ocupados": ocupados, "libres": libres})
	})

	// ======== RESERVAS (VIP) ========

	// POST /reservas { "estacionamiento_id": number }
	r.POST("/reservas", AuthMiddleware(), func(c *gin.Context) {
		var body struct {
			EstacionamientoID int `json:"estacionamiento_id"`
		}
		if err := c.BindJSON(&body); err != nil || body.EstacionamientoID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, _ := c.Get("userID")
		userID := uidVal.(int)

		// Solo VIP
		isVip, err := userIsVIP(userID)
		if err != nil || !isVip {
			c.JSON(http.StatusForbidden, gin.H{"error": "Solo usuarios VIP pueden reservar"})
			return
		}

		// ¿ya tiene activa?
		exists, err := hasActiveReservation(userID, body.EstacionamientoID)
		if err != nil {
			dbErr(c, err)
			return
		}
		if exists {
			c.JSON(http.StatusConflict, gin.H{"error": "Ya tenés una reserva activa en este estacionamiento"})
			return
		}

		_, err = db.Exec(`
		INSERT INTO reservas (user_id, estacionamiento_id, status)
		VALUES (?,?,1)
	`, userID, body.EstacionamientoID)
		if err != nil {
			dbErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"ok": true})
	})

	// DELETE /reservas { "estacionamiento_id": number }
	r.DELETE("/reservas", AuthMiddleware(), func(c *gin.Context) {
		var body struct {
			EstacionamientoID int `json:"estacionamiento_id"`
		}
		if err := c.BindJSON(&body); err != nil || body.EstacionamientoID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		uidVal, _ := c.Get("userID")
		userID := uidVal.(int)

		res, err := db.Exec(`
		UPDATE reservas
		SET status=0, canceled_at=NOW()
		WHERE user_id=? AND estacionamiento_id=? AND status=1
	`, userID, body.EstacionamientoID)
		if err != nil {
			dbErr(c, err)
			return
		}
		aff, _ := res.RowsAffected()
		if aff == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "No tenés una reserva activa para cancelar"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// GET /reservas/estado?estacionamiento_id=123
	r.GET("/reservas/estado", AuthMiddleware(), func(c *gin.Context) {
		estID, _ := strconv.Atoi(c.Query("estacionamiento_id"))
		if estID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id inválido"})
			return
		}
		uidVal, _ := c.Get("userID")
		userID := uidVal.(int)

		ok, err := hasActiveReservation(userID, estID)
		if err != nil {
			dbErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"activa": ok})
	})

	// ✅ Puerto dinámico y arranque
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("🚀 listening on port", port)
	r.Run(":" + port)
}
