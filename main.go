package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
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

// ----------- MAIN ------------

func main() {
	conectarDB()
	defer db.Close()
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

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
		c.JSON(http.StatusOK, gin.H{"token": signed})
	})

	// ============= 🚗 ESTACIONAMIENTOS ==============

	r.POST("/estacionamientos", func(c *gin.Context) {
		var in EstacionamientoNuevo
		if err := c.BindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}
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
		banos := 0
		if in.Banos != nil && *in.Banos {
			banos = 1
		}
		res, err := db.Exec(`
			INSERT INTO estacionamientos
				(duenio_id, nombre, cantidad, latitud, longitud,
				 precio_por_hora, techado, seguridad, banos, altura_max_m)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.DuenioID, in.Nombre, in.Cantidad, in.Latitud, in.Longitud,
			in.PrecioPorHora, in.Techado, seg, banos, in.AlturaMaxM,
		)
		if err != nil {
			dbErr(c, err)
			return
		}
		nuevoID, _ := res.LastInsertId()
		for _, dia := range in.Dias {
			_, _ = db.Exec(`INSERT INTO dias_atencion (estacionamiento_id, dia, desde, hasta)
			                VALUES (?, ?, ?, ?)`,
				nuevoID, dia.Dia, dia.Desde, dia.Hasta)
		}
		c.JSON(http.StatusOK, gin.H{"id": nuevoID})
	})

	r.POST("/lugares", func(c *gin.Context) {
		var req ActualizacionLugar
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}
		for i := 1; i <= req.Cantidad; i++ {
			_, _ = db.Exec(`
				INSERT INTO lugares (estacionamiento_id, numero, ocupado)
				VALUES (?, ?, ?)
				ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)`,
				req.EstacionamientoID, i, false)
		}
		c.JSON(http.StatusOK, gin.H{"mensaje": "OK"})
	})

	r.POST("/lugares/estado", func(c *gin.Context) {
		var estado EstadoLugar
		if err := c.BindJSON(&estado); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}
		_, _ = db.Exec(`UPDATE lugares SET ocupado=? WHERE estacionamiento_id=? AND numero=?`,
			estado.Ocupado, estado.EstacionamientoID, estado.Numero)
		c.JSON(http.StatusOK, gin.H{"mensaje": "OK"})
	})

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
		c.JSON(200, gin.H{"lugares": lug})
	})

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
		c.JSON(200, gin.H{"estacionamientos": list})
	})

	// ✅ Puerto dinámico
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("🚀 listening on port", port)
	r.Run(":" + port)
}
