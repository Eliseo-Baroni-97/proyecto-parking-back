package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
)

var db *sql.DB

// ----------- CONFIG/DB -------------

func getDSN() string {
	// Usa variable de entorno si está disponible
	if v := os.Getenv("MYSQL_URL"); v != "" {
		return v
	}
	// Fallback: DSN actual (puedes borrarlo si ya usas MYSQL_URL en Railway)
	return "root:tDXPIyOImvUcSPoZIpIEQwkkqpmabXMp@tcp(trolley.proxy.rlwy.net:31348)/railway?parseTime=true&charset=utf8mb4"
}

func conectarDB() {
	var err error
	dsn := getDSN()
	fmt.Println("🔌 DSN:", dsn)

	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("❌ Error al abrir la conexión:", err)
	}
	if err = db.Ping(); err != nil {
		var base string
		_ = db.QueryRow("SELECT DATABASE()").Scan(&base)
		fmt.Println("🧠 Base de datos activa:", base)
		log.Fatal("❌ No se pudo conectar a MySQL:", err)
	}
	fmt.Println("✅ Conectado a MySQL")
}

// Crea tablas si no existen (con AI correcto)
func ensureSchema() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS estacionamientos (
	  id INT NOT NULL AUTO_INCREMENT,
	  duenio_id INT NOT NULL,
	  nombre VARCHAR(100) NOT NULL,
	  cantidad INT NOT NULL,
	  latitud DECIMAL(9,6) NOT NULL,
	  longitud DECIMAL(9,6) NOT NULL,
	  creado_en TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	  PRIMARY KEY (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS lugares (
	  id INT NOT NULL AUTO_INCREMENT,
	  estacionamiento_id INT NOT NULL,
	  numero INT NOT NULL,
	  ocupado BOOLEAN NOT NULL DEFAULT FALSE,
	  PRIMARY KEY (id),
	  UNIQUE KEY ux_est_num (estacionamiento_id, numero)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS dias_atencion (
	  id INT NOT NULL AUTO_INCREMENT,
	  estacionamiento_id INT NOT NULL,
	  dia VARCHAR(20) NOT NULL,
	  desde VARCHAR(5) NOT NULL,
	  hasta VARCHAR(5) NOT NULL,
	  PRIMARY KEY (id),
	  INDEX (estacionamiento_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`)
	return err
}

// Repara la columna id de estacionamientos si no tiene AUTO_INCREMENT/PK
func ensureEstacionamientosAI() error {
	var extra sql.NullString
	err := db.QueryRow(`
	  SELECT EXTRA
	  FROM INFORMATION_SCHEMA.COLUMNS
	  WHERE TABLE_SCHEMA = DATABASE()
	    AND TABLE_NAME='estacionamientos'
	    AND COLUMN_NAME='id'`).Scan(&extra)
	if err != nil {
		return err
	}

	if !extra.Valid || !strings.Contains(strings.ToLower(extra.String), "auto_increment") {
		if _, err := db.Exec(`ALTER TABLE estacionamientos MODIFY id INT NOT NULL AUTO_INCREMENT`); err != nil {
			return err
		}
	}

	var pkCount int
	_ = db.QueryRow(`
	  SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
	  WHERE TABLE_SCHEMA = DATABASE()
	    AND TABLE_NAME='estacionamientos'
	    AND INDEX_NAME='PRIMARY'`).Scan(&pkCount)
	if pkCount == 0 {
		if _, err := db.Exec(`ALTER TABLE estacionamientos ADD PRIMARY KEY (id)`); err != nil {
			return err
		}
	}
	return nil
}

// Asegura índice único para (estacionamiento_id, numero) en lugares
func ensureLugaresUniqueIndex() error {
	var cnt int
	err := db.QueryRow(`
	  SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
	  WHERE TABLE_SCHEMA = DATABASE()
	    AND TABLE_NAME='lugares'
	    AND INDEX_NAME='ux_est_num'`).Scan(&cnt)
	if err != nil {
		return err
	}
	if cnt == 0 {
		_, err := db.Exec(`ALTER TABLE lugares ADD UNIQUE KEY ux_est_num (estacionamiento_id, numero)`)
		return err
	}
	return nil
}

// Asegura AUTO_INCREMENT para una tabla dada (id)
func ensureAI(table string) error {
	var extra sql.NullString
	if err := db.QueryRow(`
		SELECT EXTRA FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='id'`, table).Scan(&extra); err != nil {
		return err
	}
	if !extra.Valid || !strings.Contains(strings.ToLower(extra.String), "auto_increment") {
		if _, err := db.Exec(`ALTER TABLE ` + table + ` MODIFY id INT NOT NULL AUTO_INCREMENT`); err != nil {
			return err
		}
	}
	return nil
}

// Agrega columnas opcionales a estacionamientos si faltan (mantiene nombres/tipos)
func ensureEstacionamientosExtraCols() error {
	type col struct{ name, ddl string }
	cols := []col{
		{"precio_por_hora", "ADD COLUMN precio_por_hora DECIMAL(10,2) NULL"},
		{"techado", "ADD COLUMN techado ENUM('techado','media_sombra','no') NULL"},
		{"seguridad", "ADD COLUMN seguridad SET('camaras','vigilante') NULL"},
		{"banos", "ADD COLUMN banos TINYINT(1) NOT NULL DEFAULT 0"},
		{"altura_max_m", "ADD COLUMN altura_max_m DECIMAL(4,2) NULL"},
	}
	for _, c := range cols {
		var cnt int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			  AND TABLE_NAME = 'estacionamientos'
			  AND COLUMN_NAME = ?`, c.name).Scan(&cnt); err != nil {
			return err
		}
		if cnt == 0 {
			if _, err := db.Exec(`ALTER TABLE estacionamientos ` + c.ddl); err != nil {
				return err
			}
		}
	}
	return nil
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
	PrecioPorHora *float64      `json:"precio_por_hora"` // opcional
	Techado       *string       `json:"techado"`         // "techado"|"media_sombra"|"no"
	Seguridad     []string      `json:"seguridad"`       // ["camaras","vigilante"]
	Banos         *bool         `json:"banos"`           // true/false
	AlturaMaxM    *float64      `json:"altura_max_m"`    // opcional
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

type EstadoLugaresRequest struct {
	EstacionamientoID int           `json:"estacionamiento_id"`
	Lugares           []LugarSimple `json:"lugares"`
}

// ----------- HELPERS -------------

func mostrarEstadoLugares(estacionamientoID int) {
	fmt.Printf("\n🟦 Estado actual del Estacionamiento ID %d:\n", estacionamientoID)

	rows, err := db.Query(`
		SELECT numero, ocupado FROM lugares WHERE estacionamiento_id = ?
		ORDER BY numero
	`, estacionamientoID)
	if err != nil {
		log.Println("❌ Error al consultar estado:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var numero int
		var ocupado bool
		err := rows.Scan(&numero, &ocupado)
		if err != nil {
			log.Println("❌ Error al leer fila:", err)
			continue
		}
		estado := "🟢 Libre"
		if ocupado {
			estado = "🔴 Ocupado"
		}
		fmt.Printf("Lugar %d: %s\n", numero, estado)
	}
	fmt.Println("------------------------------------")
}

func dbErr(c *gin.Context, err error) {
	if os.Getenv("DEBUG") == "1" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DB error"})
	}
}

// ----------- MAIN -------------

func main() {
	conectarDB()
	defer db.Close()

	// Migraciones mínimas + reparaciones
	if err := ensureSchema(); err != nil {
		log.Fatal("ensureSchema:", err)
	}
	if err := ensureEstacionamientosAI(); err != nil {
		log.Fatal("ensureEstacionamientosAI:", err)
	}
	// Asegurar AI genérico en las otras tablas por si ya existían
	if err := ensureAI("lugares"); err != nil {
		log.Fatal("ensureAI(lugares):", err)
	}
	if err := ensureAI("dias_atencion"); err != nil {
		log.Fatal("ensureAI(dias_atencion):", err)
	}
	// Agregar columnas nuevas si faltan (manteniendo nombres/tipos)
	if err := ensureEstacionamientosExtraCols(); err != nil {
		log.Fatal("ensureEstacionamientosExtraCols:", err)
	}
	if err := ensureLugaresUniqueIndex(); err != nil {
		log.Fatal("ensureLugaresUniqueIndex:", err)
	}

	// Muestra el DDL de la tabla (útil para confirmar en Railway)
	var tbl, ddl string
	if err := db.QueryRow("SHOW CREATE TABLE estacionamientos").Scan(&tbl, &ddl); err == nil {
		fmt.Println("📐 DDL remoto estacionamientos:\n", ddl)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// 🔎 Debug temporal (borrarlo luego)
	r.GET("/_debug/db", func(c *gin.Context) {
		var dbname, host, version, sqlmode string
		var port int
		_ = db.QueryRow("SELECT DATABASE(), @@hostname, @@port, @@version, @@sql_mode").
			Scan(&dbname, &host, &port, &version, &sqlmode)

		var t1, ddl1, t2, ddl2, t3, ddl3 string
		_ = db.QueryRow("SHOW CREATE TABLE estacionamientos").Scan(&t1, &ddl1)
		_ = db.QueryRow("SHOW CREATE TABLE lugares").Scan(&t2, &ddl2)
		_ = db.QueryRow("SHOW CREATE TABLE dias_atencion").Scan(&t3, &ddl3)

		c.JSON(200, gin.H{
			"db": dbname, "host": host, "port": port, "version": version, "sql_mode": sqlmode,
			"ddl_estacionamientos": ddl1,
			"ddl_lugares":          ddl2,
			"ddl_dias_atencion":    ddl3,
		})
	})

	// 🚗 Crear nuevo estacionamiento
	r.POST("/estacionamientos", func(c *gin.Context) {
		var in EstacionamientoNuevo
		if err := c.BindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		log.Printf("🧪 Datos recibidos: DuenioID=%d, Nombre=%s, Cantidad=%d, Lat=%.4f, Lng=%.4f",
			in.DuenioID, in.Nombre, in.Cantidad, in.Latitud, in.Longitud)

		// Seguridad (SET) -> CSV validado
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
		// Banos (bool) -> TINYINT(1)
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
			log.Println("❌ Error al guardar estacionamiento:", err)
			dbErr(c, err)
			return
		}

		nuevoID, err := res.LastInsertId()
		if err != nil {
			log.Println("❌ Error al obtener ID insertado:", err)
			dbErr(c, err)
			return
		}

		// Días
		for _, dia := range in.Dias {
			_, err := db.Exec(`
				INSERT INTO dias_atencion (estacionamiento_id, dia, desde, hasta)
				VALUES (?, ?, ?, ?)`,
				nuevoID, dia.Dia, dia.Desde, dia.Hasta,
			)
			if err != nil {
				log.Println("❌ Error al guardar día:", err)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"mensaje": "Estacionamiento creado correctamente",
			"id":      nuevoID,
		})
	})

	// 🅿️ Crear lugares para un estacionamiento
	r.POST("/lugares", func(c *gin.Context) {
		var req ActualizacionLugar
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		fmt.Printf("\n📥 Alta de estacionamiento ID %d con %d lugares...\n", req.EstacionamientoID, req.Cantidad)
		for i := 1; i <= req.Cantidad; i++ {
			_, err := db.Exec(`
				INSERT INTO lugares (estacionamiento_id, numero, ocupado)
				VALUES (?, ?, ?)
				ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)`,
				req.EstacionamientoID, i, false,
			)
			if err != nil {
				log.Println("❌ Error al insertar lugar:", err)
			}
		}

		mostrarEstadoLugares(req.EstacionamientoID)
		c.JSON(http.StatusOK, gin.H{"mensaje": "Estacionamiento creado con lugares", "datos": req})
	})

	// 🔁 Actualizar lugar individual
	r.POST("/lugares/estado", func(c *gin.Context) {
		var estado EstadoLugar
		if err := c.BindJSON(&estado); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		_, err := db.Exec(`
			UPDATE lugares SET ocupado = ?
			WHERE estacionamiento_id = ? AND numero = ?`,
			estado.Ocupado, estado.EstacionamientoID, estado.Numero,
		)
		if err != nil {
			log.Println("❌ Error al actualizar lugar:", err)
			dbErr(c, err)
			return
		}

		mostrarEstadoLugares(estado.EstacionamientoID)
		c.JSON(http.StatusOK, gin.H{"mensaje": "Estado actualizado correctamente", "datos": estado})
	})

	// 💾 Guardar múltiples lugares
	r.POST("/lugares/guardar-multiples", func(c *gin.Context) {
		var req EstadoLugaresRequest
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		for _, lugar := range req.Lugares {
			_, err := db.Exec(`
				INSERT INTO lugares (estacionamiento_id, numero, ocupado)
				VALUES (?, ?, ?)
				ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)`,
				req.EstacionamientoID, lugar.Numero, lugar.Ocupado,
			)
			if err != nil {
				log.Println("❌ Error al guardar lugar:", err)
			}
		}

		mostrarEstadoLugares(req.EstacionamientoID)
		c.JSON(http.StatusOK, gin.H{"mensaje": "Lugares guardados correctamente", "total": len(req.Lugares)})
	})

	// 🔍 Consultar estado (lista de lugares)
	r.GET("/estado/:id", func(c *gin.Context) {
		estacionamientoID := c.Param("id")

		rows, err := db.Query(`
			SELECT numero, ocupado FROM lugares WHERE estacionamiento_id = ?
			ORDER BY numero`, estacionamientoID)
		if err != nil {
			log.Println("❌ Error al obtener lugares:", err)
			dbErr(c, err)
			return
		}
		defer rows.Close()

		var lugares []LugarSimple
		for rows.Next() {
			var lugar LugarSimple
			if err := rows.Scan(&lugar.Numero, &lugar.Ocupado); err != nil {
				continue
			}
			lugares = append(lugares, lugar)
		}

		c.JSON(http.StatusOK, gin.H{"lugares": lugares})
	})

	// 📅 Días de atención
	r.GET("/estacionamientos/:id/dias", func(c *gin.Context) {
		id := c.Param("id")

		rows, err := db.Query(`
			SELECT dia, desde, hasta 
			FROM dias_atencion 
			WHERE estacionamiento_id = ?`, id)
		if err != nil {
			log.Println("❌ Error al consultar días:", err)
			dbErr(c, err)
			return
		}
		defer rows.Close()

		var dias []DiaAtencion
		for rows.Next() {
			var dia DiaAtencion
			if err := rows.Scan(&dia.Dia, &dia.Desde, &dia.Hasta); err != nil {
				log.Println("❌ Error al escanear fila:", err)
				continue
			}
			dias = append(dias, dia)
		}
		c.JSON(http.StatusOK, gin.H{"dias": dias})
	})

	// 📃 Obtener un estacionamiento por ID (con resumen)
	// 📃 Detalle completo + resumen
	r.GET("/estacionamientos/:id", func(c *gin.Context) {
		id := c.Param("id")

		var e struct {
			ID            int64    `json:"id"`
			DuenioID      int      `json:"duenio_id"`
			Nombre        string   `json:"nombre"`
			Cantidad      int      `json:"cantidad"`
			Latitud       float64  `json:"latitud"`
			Longitud      float64  `json:"longitud"`
			PrecioPorHora *float64 `json:"precio_por_hora"`
			Techado       *string  `json:"techado"`
			Seguridad     *string  `json:"seguridad"` // CSV del SET: "camaras,vigilante"
			Banos         int      `json:"banos"`     // 0/1
			AlturaMaxM    *float64 `json:"altura_max_m"`
		}
		if err := db.QueryRow(`
		SELECT id, duenio_id, nombre, cantidad, latitud, longitud,
		       precio_por_hora, techado, seguridad, banos, altura_max_m
		FROM estacionamientos
		WHERE id = ?`, id).
			Scan(&e.ID, &e.DuenioID, &e.Nombre, &e.Cantidad, &e.Latitud, &e.Longitud,
				&e.PrecioPorHora, &e.Techado, &e.Seguridad, &e.Banos, &e.AlturaMaxM); err != nil {
			dbErr(c, err)
			return
		}

		var ocupados int
		_ = db.QueryRow(`SELECT COUNT(*) FROM lugares WHERE estacionamiento_id=? AND ocupado=1`, id).Scan(&ocupados)

		c.JSON(200, gin.H{
			"estacionamiento": e,
			"resumen":         gin.H{"total": e.Cantidad, "ocupados": ocupados, "libres": e.Cantidad - ocupados},
		})
	})

	// 👀 Listar todos (o cercanos con lat/lng & km)
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

		lat := c.Query("lat")
		lng := c.Query("lng")
		km := c.DefaultQuery("km", "3")

		var rows *sql.Rows
		var err error

		if lat != "" && lng != "" {
			// bounding box aprox: 1° lat ~111km, 1° lng ~111km*cos(lat)
			q := `
			WITH agg AS (
			  SELECT e.id, e.nombre, e.latitud, e.longitud,
			         e.cantidad AS total,
			         SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END) AS ocupados
			  FROM estacionamientos e
			  LEFT JOIN lugares l ON l.estacionamiento_id = e.id
			  GROUP BY e.id
			)
			SELECT id, nombre, latitud, longitud, total,
			       IFNULL(ocupados,0) AS ocupados,
			       (total-IFNULL(ocupados,0)) AS libres
			FROM agg
			WHERE latitud  BETWEEN ?-?/111.0 AND ?+?/111.0
			  AND longitud BETWEEN ?-?/(111.0*COS(RADIANS(?))) AND ?+?/(111.0*COS(RADIANS(?)))
			ORDER BY id DESC;`
			rows, err = db.Query(q, lat, km, lat, km, lng, km, lat, lng, km, lat)
		} else {
			q := `
			SELECT e.id, e.nombre, e.latitud, e.longitud,
			       e.cantidad AS total,
			       COALESCE(SUM(CASE WHEN l.ocupado=1 THEN 1 ELSE 0 END),0) AS ocupados
			FROM estacionamientos e
			LEFT JOIN lugares l ON l.estacionamiento_id = e.id
			GROUP BY e.id, e.nombre, e.latitud, e.longitud, e.cantidad
			ORDER BY e.id DESC;`
			rows, err = db.Query(q)
		}
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
	log.Println("🚀 Servidor escuchando en puerto", port)
	r.Run(":" + port)
}
