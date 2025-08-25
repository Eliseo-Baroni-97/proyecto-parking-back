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
}

// ----------- HELPERS -------------

func dbErr(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// ========= MIDDLEWARE (leer user_id desde el JWT) =============

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

		// Validar expiración (por si Parse no lo hace)
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

// Helper: ¿el estacionamiento le pertenece al usuario?
func ownsEstacionamiento(estID, userID int) bool {
	var n int
	err := db.QueryRow(`SELECT COUNT(1) FROM estacionamientos WHERE id=? AND duenio_id=?`, estID, userID).Scan(&n)
	return err == nil && n > 0
}

// ----------- MAIN ------------

func main() {
	conectarDB()
	defer db.Close()
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*") // o tu dominio
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

		// Generar hash
		hash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Println("❌ Error al generar hash:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
			return
		}

		// Insertar en la base
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

	// ================= 🔐 LOGIN ==================
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

		// Buscar usuario
		var u User
		err := db.QueryRow(`SELECT id, email, password_hash FROM usuarios WHERE email = ?`, payload.Email).
			Scan(&u.ID, &u.Email, &u.PasswordHash)

		if err != nil {
			fmt.Println("❌ No se encontró usuario:", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Credenciales inválidas"})
			return
		}

		fmt.Println("🔎 Usuario encontrado:", u.Email, "hash:", u.PasswordHash)

		// Comparar password
		if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(payload.Password)); err != nil {
			fmt.Println("❌ La contraseña no coincide:", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Credenciales inválidas"})
			return
		}

		fmt.Println("✅ Password correcta, generando token...")

		fmt.Println("🔧 JWT_SECRET en runtime:", os.Getenv("JWT_SECRET"))

		secret := os.Getenv("JWT_SECRET")

		if secret == "" {
			fmt.Println("❌ JWT_SECRET no definido")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "JWT no configurado"})
			return
		}

		claims := jwt.MapClaims{
			"user_id": u.ID,
			"email":   u.Email,
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
			"user_id": u.ID, // ← DEVOLVEMOS TAMBIÉN EL ID DEL USUARIO
		})
	})

	// ============= 🚗 ESTACIONAMIENTOS ==============

	// PROTEGER con AuthMiddleware
	r.POST("/estacionamientos", AuthMiddleware(), func(c *gin.Context) {
		var in EstacionamientoNuevo
		if err := c.BindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		// ✅ Tomar el userID desde el token (seguro)
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

		// ✅ 201 Created
		c.JSON(http.StatusCreated, gin.H{"id": nuevoID})
	})

	// =================== 🔎 LISTAR MIS ESTACIONAMIENTOS =================
	r.GET("/mis-estacionamientos", AuthMiddleware(), func(c *gin.Context) {
		// obtener el userID desde el contexto
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

	// Reemplazo de POST /lugares (protegido + check de dueño)
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

	// ✅ BLOQUE NUEVO: PROTEGIDO + CHECK DE DUEÑO
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

	// 🔎 Detalle público de un estacionamiento (para la app de usuarios)
	r.GET("/estacionamientos/:id/detalle", func(c *gin.Context) {
		idStr := c.Param("id")

		var (
			id        int
			nombre    string
			lat, lng  float64
			cantidad  int
			precio    sql.NullFloat64
			techado   sql.NullString
			seguridad sql.NullString
			banos     sql.NullInt64
			alturaMax sql.NullFloat64
		)

		err := db.QueryRow(`
        SELECT id, nombre, latitud, longitud, cantidad,
               precio_por_hora, techado, seguridad, banos, altura_max_m
        FROM estacionamientos
        WHERE id = ?
    `, idStr).Scan(&id, &nombre, &lat, &lng, &cantidad,
			&precio, &techado, &seguridad, &banos, &alturaMax)
		if err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "No existe"})
				return
			}
			dbErr(c, err)
			return
		}

		// Días de atención (orden lun..dom)
		rows, err := db.Query(`
        SELECT dia, desde, hasta
        FROM dias_atencion
        WHERE estacionamiento_id = ?
        ORDER BY FIELD(dia,'lun','mar','mie','jue','vie','sab','dom')`, id)
		if err != nil {
			dbErr(c, err)
			return
		}
		defer rows.Close()

		dias := make([]DiaAtencion, 0, 7)
		for rows.Next() {
			var d DiaAtencion
			if err := rows.Scan(&d.Dia, &d.Desde, &d.Hasta); err == nil {
				dias = append(dias, d)
			}
		}

		// Resumen de lugares
		var total, ocupados int
		err = db.QueryRow(`
        SELECT COALESCE(COUNT(*),0) AS total,
               COALESCE(SUM(CASE WHEN ocupado=1 THEN 1 ELSE 0 END),0) AS ocupados
        FROM lugares
        WHERE estacionamiento_id = ?`, id).Scan(&total, &ocupados)
		if err != nil {
			dbErr(c, err)
			return
		}
		libres := total - ocupados

		// Normalizar seguridad
		segVal := "ninguna"
		if seguridad.Valid && seguridad.String != "" {
			s := strings.ToLower(seguridad.String)
			if strings.Contains(s, "camaras") {
				segVal = "camaras"
			}
			if strings.Contains(s, "vigilante") {
				segVal = "vigilante"
			}
		}

		// Horario simple: si todos 24 hs
		horario := ""
		if len(dias) > 0 {
			all24 := true
			for _, d := range dias {
				if !(d.Desde == "00:00" && d.Hasta == "23:59") {
					all24 = false
					break
				}
			}
			if all24 {
				horario = "Todos los días 24 hs"
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"id":       id,
			"nombre":   nombre,
			"latitud":  lat,
			"longitud": lng,
			"total":    total,
			"ocupados": ocupados,
			"libres":   libres,
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
			"seguridad": segVal,
			"banos":     banos.Valid && banos.Int64 == 1,
			"altura_max_m": func() *float64 {
				if alturaMax.Valid {
					return &alturaMax.Float64
				}
				return nil
			}(),
			"horario": horario,
			"dias":    dias,
		})
	})

	// 🧩 Resumen público chiquito (polling)
	r.GET("/estacionamientos/:id/resumen", func(c *gin.Context) {
		idStr := c.Param("id")
		var total, ocupados int
		if err := db.QueryRow(`
        SELECT COALESCE(COUNT(*),0),
               COALESCE(SUM(CASE WHEN ocupado=1 THEN 1 ELSE 0 END),0)
        FROM lugares WHERE estacionamiento_id = ?`, idStr).Scan(&total, &ocupados); err != nil {
			dbErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"total":    total,
			"ocupados": ocupados,
			"libres":   total - ocupados,
		})
	})

	// 📍 Cerca de (lat,lng) en un radio en km. Ej: /estacionamientos/cerca?lat=..&lng=..&km=0.8
	r.GET("/estacionamientos/cerca", func(c *gin.Context) {
		latStr := c.Query("lat")
		lngStr := c.Query("lng")
		kmStr := c.DefaultQuery("km", "1")

		lat0, err1 := strconv.ParseFloat(latStr, 64)
		lng0, err2 := strconv.ParseFloat(lngStr, 64)
		radioKm, err3 := strconv.ParseFloat(kmStr, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Parámetros inválidos"})
			return
		}

		type Item struct {
			ID          int64   `json:"id"`
			Nombre      string  `json:"nombre"`
			Latitud     float64 `json:"latitud"`
			Longitud    float64 `json:"longitud"`
			Total       int     `json:"total"`
			Ocupados    int     `json:"ocupados"`
			Libres      int     `json:"libres"`
			DistanciaKM float64 `json:"distancia_km"`
		}

		rows, err := db.Query(`
      SELECT
        e.id,
        e.nombre,
        e.latitud,
        e.longitud,
        e.cantidad,
        COALESCE(SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END),0) AS ocupados,
        (6371 * ACOS(
          COS(RADIANS(?)) * COS(RADIANS(e.latitud)) *
          COS(RADIANS(e.longitud) - RADIANS(?)) +
          SIN(RADIANS(?)) * SIN(RADIANS(e.latitud))
        )) AS distancia_km
      FROM estacionamientos e
      LEFT JOIN lugares l ON l.estacionamiento_id = e.id
      GROUP BY e.id
      HAVING distancia_km <= ?
      ORDER BY distancia_km ASC
    `, lat0, lng0, lat0, radioKm)
		if err != nil {
			dbErr(c, err)
			return
		}
		defer rows.Close()

		var list []Item
		for rows.Next() {
			var it Item
			var cantidad int
			if err := rows.Scan(&it.ID, &it.Nombre, &it.Latitud, &it.Longitud, &cantidad, &it.Ocupados, &it.DistanciaKM); err == nil {
				it.Total = cantidad
				it.Libres = cantidad - it.Ocupados
				list = append(list, it)
			}
		}
		c.JSON(http.StatusOK, gin.H{"estacionamientos": list})
	})

	// 📍 Lista pública de estacionamientos (mapa)
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
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("🚀 listening on port", port)
	r.Run(":" + port)
	_ = fmt.Println
}
