#!/bin/bash
service postgresql start
sudo -u postgres psql -c "CREATE DATABASE kb_hub;"
sudo -u postgres psql -c "CREATE USER postgres WITH PASSWORD 'postgres';"
sudo -u postgres psql -c "ALTER USER postgres WITH SUPERUSER;"
sudo -u postgres psql -c "CREATE EXTENSION IF NOT EXISTS vector;"