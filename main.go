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
	// Fallback: tu DSN actual (puedes borrarlo si ya usas MYSQL_URL en Railway)
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

// ----------- TIPOS -------------

type DiaAtencion struct {
	Dia   string `json:"dia"`
	Desde string `json:"desde"`
	Hasta string `json:"hasta"`
}

type EstacionamientoNuevo struct {
	DuenioID int           `json:"duenio_id"`
	Nombre   string        `json:"nombre"`
	Cantidad int           `json:"cantidad"`
	Latitud  float64       `json:"latitud"`
	Longitud float64       `json:"longitud"`
	Dias     []DiaAtencion `json:"dias"`
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
		var req EstacionamientoNuevo
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		log.Printf("🧪 Datos recibidos: DuenioID=%d, Nombre=%s, Cantidad=%d, Lat=%.4f, Lng=%.4f",
			req.DuenioID, req.Nombre, req.Cantidad, req.Latitud, req.Longitud)

		res, err := db.Exec(`
			INSERT INTO estacionamientos (duenio_id, nombre, cantidad, latitud, longitud)
			VALUES (?, ?, ?, ?, ?)`,
			req.DuenioID, req.Nombre, req.Cantidad, req.Latitud, req.Longitud,
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

		for _, dia := range req.Dias {
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

	// 🔍 Consultar estado
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

	// ✅ Puerto dinámico
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("🚀 Servidor escuchando en puerto", port)
	r.Run(":" + port)
}
