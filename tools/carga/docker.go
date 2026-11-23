package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// entorno lleva lo que se ha creado en docker para borrarlo al acabar.
type entorno struct {
	mu           sync.Mutex
	contenedores []string
	red          string
	// imagen es la que ha construido la prueba y se borra al acabar.
	imagen string
}

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (e *entorno) crearRed(ctx context.Context, nombre string) error {
	if _, err := docker(ctx, "network", "create", nombre); err != nil {
		return err
	}
	e.mu.Lock()
	e.red = nombre
	e.mu.Unlock()
	return nil
}

// arrancar hace docker run -d y apunta el contenedor para borrarlo.
func (e *entorno) arrancar(ctx context.Context, nombre string, args ...string) error {
	e.mu.Lock()
	e.contenedores = append(e.contenedores, nombre)
	e.mu.Unlock()
	_, err := docker(ctx, append([]string{"run", "-d", "--name", nombre}, args...)...)
	return err
}

// limpiar borra los contenedores y la red creados, aunque ctx esté cancelado.
func (e *entorno) limpiar() {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := len(e.contenedores) - 1; i >= 0; i-- {
		if _, err := docker(ctx, "rm", "-f", "-v", e.contenedores[i]); err != nil {
			fmt.Fprintln(os.Stderr, "borrar contenedor:", err)
		}
	}
	e.contenedores = nil
	if e.red != "" {
		if _, err := docker(ctx, "network", "rm", e.red); err != nil {
			fmt.Fprintln(os.Stderr, "borrar red:", err)
		}
		e.red = ""
	}
	if e.imagen != "" {
		if _, err := docker(ctx, "rmi", e.imagen); err != nil {
			fmt.Fprintln(os.Stderr, "borrar imagen:", err)
		}
		e.imagen = ""
	}
}

// puerto da la dirección del anfitrión donde docker publicó el puerto del contenedor.
func puerto(ctx context.Context, contenedor, p string) (string, error) {
	out, err := docker(ctx, "port", contenedor, p)
	if err != nil {
		return "", err
	}
	for l := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(l, "127.0.0.1:") {
			return l, nil
		}
	}
	return "", fmt.Errorf("el puerto %s de %s no está publicado en 127.0.0.1: %q", p, contenedor, out)
}

// construir hace la imagen del Dockerfile con el código de ref, o con el
// directorio de trabajo si ref está vacío.
func construir(ctx context.Context, imagen, ref string) error {
	cmd := exec.CommandContext(ctx, "docker", "build", "-q", "-t", imagen, ".")
	if ref != "" {
		var errb bytes.Buffer
		archive := exec.CommandContext(ctx, "git", "archive", "--format=tar", ref)
		archive.Stderr = &errb
		tar, err := archive.Output()
		if err != nil {
			return fmt.Errorf("git archive %s: %v: %s", ref, err, strings.TrimSpace(errb.String()))
		}
		cmd = exec.CommandContext(ctx, "docker", "build", "-q", "-t", imagen, "-")
		cmd.Stdin = bytes.NewReader(tar)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("construir la imagen: %w", err)
	}
	return nil
}
