#!/bin/bash
sudo -u postgres psql -d kb_hub -c "CREATE EXTENSION IF NOT EXISTS vector;"