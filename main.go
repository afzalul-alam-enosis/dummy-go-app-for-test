package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type User struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"type:varchar(255)"`
	Email     string    `json:"email" gorm:"type:varchar(255);uniqueIndex"`
	CreatedAt time.Time `json:"created_at"`
}

type App struct {
	db    *gorm.DB
	redis *redis.Client
}

type CreateUserRequest struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email" binding:"required,email"`
}

func main() {
	db := connectMySQL()
	redisClient := connectRedis()

	// Create table for testing.
	if err := db.AutoMigrate(&User{}); err != nil {
		log.Fatal("failed to migrate database:", err)
	}

	app := &App{
		db:    db,
		redis: redisClient,
	}

	router := gin.Default()

	router.GET("/health", app.health)

	router.GET("/health/mysql", app.healthMySQL)
	router.GET("/health/redis", app.healthRedis)

	router.POST("/users", app.createUser)
	router.GET("/users/:id", app.getUser)

	router.GET("/cache/:key", app.getCache)
	router.POST("/cache/:key", app.setCache)
	router.DELETE("/cache/:key", app.deleteCache)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("server running on port %s", port)

	if err := router.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func connectMySQL() *gorm.DB {
	host := getEnv("MYSQL_HOST", "localhost")
	port := getEnv("MYSQL_PORT", "3306")
	user := getEnv("MYSQL_USER", "root")
	password := getEnv("MYSQL_PASSWORD", "")
	database := getEnv("MYSQL_DATABASE", "testdb")

	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		user,
		password,
		host,
		port,
		database,
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("failed to connect to MySQL:", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal("failed to get SQL DB:", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	log.Println("connected to MySQL")

	return db
}

func connectRedis() *redis.Client {
	host := getEnv("REDIS_HOST", "localhost")
	port := getEnv("REDIS_PORT", "6379")
	password := os.Getenv("REDIS_PASSWORD")

	client := redis.NewClient(&redis.Options{
		Addr:     host + ":" + port,
		Password: password,
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatal("failed to connect to Redis:", err)
	}

	log.Println("connected to Redis")

	return client
}

func (app *App) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}

func (app *App) healthMySQL(c *gin.Context) {
	sqlDB, err := app.db.DB()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
		})
		return
	}

	if err := sqlDB.Ping(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "error",
			"mysql":  "unavailable",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"mysql":  "connected",
	})
}

func (app *App) healthRedis(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := app.redis.Ping(ctx).Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "error",
			"redis":  "unavailable",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"redis":  "connected",
	})
}

func (app *App) createUser(c *gin.Context) {
	var request CreateUserRequest

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	user := User{
		Name:  request.Name,
		Email: request.Email,
	}

	if err := app.db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to create user",
		})
		return
	}

	c.JSON(http.StatusCreated, user)
}

func (app *App) getUser(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid user ID",
		})
		return
	}

	var user User

	if err := app.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "user not found",
		})
		return
	}

	c.JSON(http.StatusOK, user)
}

func (app *App) setCache(c *gin.Context) {
	key := c.Param("key")

	var request struct {
		Value string `json:"value" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	ctx := c.Request.Context()

	if err := app.redis.Set(
		ctx,
		key,
		request.Value,
		5*time.Minute,
	).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to store value",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key":   key,
		"value": request.Value,
	})
}

func (app *App) getCache(c *gin.Context) {
	key := c.Param("key")

	ctx := c.Request.Context()

	value, err := app.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "key not found",
		})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to read cache",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key":   key,
		"value": value,
	})
}

func (app *App) deleteCache(c *gin.Context) {
	key := c.Param("key")

	ctx := c.Request.Context()

	if err := app.redis.Del(ctx, key).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to delete cache",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key":     key,
		"deleted": true,
	})
}

func getEnv(key string, defaultValue string) string {
	value := os.Getenv(key)

	if value == "" {
		return defaultValue
	}

	return value
}
