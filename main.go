package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
)

var db *sql.DB

func conectarDB() {
	var err error

	// ✅ Usamos directamente la URL que nos da Railway
	dsn := os.Getenv("MYSQL_URL")

	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("Error al abrir la conexión:", err)
	}
	if err = db.Ping(); err != nil {
		log.Fatal("No se pudo conectar a MySQL:", err)
	}
	fmt.Println("✅ Conectado a MySQL")
}

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

func main() {
	conectarDB()
	r := gin.Default()

	// 🚗 Crear nuevo estacionamiento con días de atención
	r.POST("/estacionamientos", func(c *gin.Context) {
		var req EstacionamientoNuevo
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		res, err := db.Exec(`
			INSERT INTO estacionamientos (duenio_id, nombre, cantidad, latitud, longitud)
			VALUES (?, ?, ?, ?, ?)
		`, req.DuenioID, req.Nombre, req.Cantidad, req.Latitud, req.Longitud)
		if err != nil {
			log.Println("❌ Error al guardar estacionamiento:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "DB error"})
			return
		}

		estacionamientoID, _ := res.LastInsertId()

		for _, dia := range req.Dias {
			_, err := db.Exec(`
				INSERT INTO dias_atencion (estacionamiento_id, dia, desde, hasta)
				VALUES (?, ?, ?, ?)
			`, estacionamientoID, dia.Dia, dia.Desde, dia.Hasta)
			if err != nil {
				log.Println("❌ Error al guardar día:", err)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"mensaje": "Estacionamiento creado correctamente",
			"id":      estacionamientoID,
		})
	})

	// 🧱 Crear lugares iniciales
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
				ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)
			`, req.EstacionamientoID, i, false)
			if err != nil {
				log.Println("❌ Error al insertar lugar:", err)
			}
		}

		mostrarEstadoLugares(req.EstacionamientoID)

		c.JSON(http.StatusOK, gin.H{
			"mensaje": "Estacionamiento creado con lugares",
			"datos":   req,
		})
	})

	// 🔁 Actualizar estado de un lugar
	r.POST("/lugares/estado", func(c *gin.Context) {
		var estado EstadoLugar
		if err := c.BindJSON(&estado); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Formato inválido"})
			return
		}

		_, err := db.Exec(`
			UPDATE lugares SET ocupado = ?
			WHERE estacionamiento_id = ? AND numero = ?
		`, estado.Ocupado, estado.EstacionamientoID, estado.Numero)
		if err != nil {
			log.Println("❌ Error al actualizar lugar:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "DB error"})
			return
		}

		mostrarEstadoLugares(estado.EstacionamientoID)

		c.JSON(http.StatusOK, gin.H{
			"mensaje": "Estado actualizado correctamente",
			"datos":   estado,
		})
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
				ON DUPLICATE KEY UPDATE ocupado = VALUES(ocupado)
			`, req.EstacionamientoID, lugar.Numero, lugar.Ocupado)
			if err != nil {
				log.Println("❌ Error al guardar lugar:", err)
			}
		}

		mostrarEstadoLugares(req.EstacionamientoID)

		c.JSON(http.StatusOK, gin.H{
			"mensaje": "Lugares guardados correctamente",
			"total":   len(req.Lugares),
		})
	})

	// 🔍 Obtener estado de todos los lugares
	r.GET("/estado/:id", func(c *gin.Context) {
		estacionamientoID := c.Param("id")

		rows, err := db.Query(`
			SELECT numero, ocupado FROM lugares WHERE estacionamiento_id = ?
			ORDER BY numero
		`, estacionamientoID)
		if err != nil {
			log.Println("❌ Error al obtener lugares:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "DB error"})
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

		c.JSON(http.StatusOK, gin.H{
			"lugares": lugares,
		})
	})

	r.Run(":8080")
}
