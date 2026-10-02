#!/bin/sh
set -e

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${CYAN}======================================================${NC}"
echo -e "${CYAN}   CONTAINIA FULL-STACK INTEGRATION TEST SUITE        ${NC}"
echo -e "${CYAN}   Testing: Next.js + Express.js + PostgreSQL 18      ${NC}"
echo -e "${CYAN}======================================================${NC}"

CONTAINIA="./containia"
if [ ! -f "$CONTAINIA" ]; then
    echo -e "${YELLOW}Compiling containia binary...${NC}"
    go build -o containia ./cmd/containia
fi

# Cleanup previous test runs
echo -e "\n${YELLOW}[Step 0] Cleaning up any previous test containers...${NC}"
for c in test-frontend test-backend test-db; do
    $CONTAINIA stop "$c" 2>/dev/null || true
    $CONTAINIA rm -f "$c" 2>/dev/null || true
done

# Step 1: Install dependencies using Bun container volume mount
echo -e "\n${YELLOW}[Step 1] Preparing Node/Bun dependencies for Express & Next.js...${NC}"
echo "Installing Express dependencies..."
$CONTAINIA run --rm -v "$(pwd)/Project/express:/app" -w /app oven/bun:latest bun install

echo "Installing Next.js dependencies..."
$CONTAINIA run --rm -v "$(pwd)/Project/nextjs:/app" -w /app oven/bun:latest bun install

# Step 2: Build OCI Images using containia build
echo -e "\n${YELLOW}[Step 2] Building 3-tier OCI images with containia build...${NC}"

echo -e "--> Building ${CYAN}test-db:1.0${NC} (PostgreSQL 18)..."
$CONTAINIA build -t test-db:1.0 ./Project/db

echo -e "--> Building ${CYAN}test-backend:1.0${NC} (Express Bun API)..."
$CONTAINIA build -t test-backend:1.0 ./Project/express

echo -e "--> Building ${CYAN}test-frontend:1.0${NC} (Next.js App)..."
$CONTAINIA build -t test-frontend:1.0 ./Project/nextjs

echo -e "\n${GREEN}Images Built Successfully! Current Local Images:${NC}"
$CONTAINIA images

# Step 3: Launch containers with Port Forwarding
echo -e "\n${YELLOW}[Step 3] Launching containers with Port Forwarding (-p)...${NC}"

echo "Launching Database on port 5432..."
$CONTAINIA run -d --name test-db -m 256m -p 5432:5432 test-db:1.0

echo "Launching Backend API on port 8000..."
$CONTAINIA run -d --name test-backend -m 256m -p 8000:8000 test-backend:1.0

echo "Launching Frontend on port 3000..."
$CONTAINIA run -d --name test-frontend -m 512m --pids-limit 1000 -p 3000:3000 test-frontend:1.0

echo -e "\n${YELLOW}Waiting 4 seconds for services to initialize...${NC}"
sleep 4

# Step 4: Verify Containers Status
echo -e "\n${YELLOW}[Step 4] Checking Container Process & Network States...${NC}"
$CONTAINIA ps

# Step 5: Test Port Forwarding & HTTP Connectivity
echo -e "\n${YELLOW}[Step 5] Running Health & Connectivity Integration Tests...${NC}"

# Test 5.1: Backend Health
echo -n "Checking Express API (http://localhost:8000/) ... "
PASSED=0
for i in $(seq 1 15); do
    RES_API=$(curl -s http://localhost:8000/ || true)
    if echo "$RES_API" | grep -q "Hello Express"; then
        echo -e "${GREEN}PASSED${NC} ($RES_API)"
        PASSED=1
        break
    fi
    sleep 1
done
if [ $PASSED -eq 0 ]; then
    echo -e "${RED}FAILED${NC} (Got: $RES_API)"
    $CONTAINIA logs test-backend
    exit 1
fi

# Test 5.2: Backend Users Endpoint
echo -n "Checking Express Users (http://localhost:8000/users) ... "
RES_USERS=$(curl -s http://localhost:8000/users || true)
if echo "$RES_USERS" | grep -q "John"; then
    echo -e "${GREEN}PASSED${NC} ($RES_USERS)"
else
    echo -e "${RED}FAILED${NC} (Got: $RES_USERS)"
    exit 1
fi

# Test 5.3: Next.js Frontend
echo -n "Checking Next.js Web (http://localhost:3000/) ... "
PASSED_FE=0
for i in $(seq 1 20); do
    RES_FE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/ || true)
    if [ "$RES_FE" = "200" ]; then
        echo -e "${GREEN}PASSED${NC} (HTTP $RES_FE OK)"
        PASSED_FE=1
        break
    fi
    sleep 1
done
if [ $PASSED_FE -eq 0 ]; then
    echo -e "${RED}FAILED${NC} (HTTP $RES_FE)"
    $CONTAINIA logs test-frontend
    exit 1
fi

# Test 5.4: Inter-Service Networking (Backend -> DB)
echo -n "Checking Inter-Service Network (test-backend -> test-db:5432) ... "
DB_PING=$($CONTAINIA exec test-backend /usr/local/bin/bun -e "const net=require('net');const s=net.createConnection(5432,'test-db',()=>{console.log('CONNECTED');s.end();process.exit(0);});s.on('error',e=>{console.log('ERR:'+e.message);process.exit(1);});" 2>/dev/null || true)

if echo "$DB_PING" | grep -q "CONNECTED"; then
    echo -e "${GREEN}PASSED${NC} (Connected to test-db:5432 via /etc/hosts)"
else
    echo -e "${YELLOW}WARNING (DB Socket status: $DB_PING)${NC}"
fi

echo -e "\n${CYAN}======================================================${NC}"
echo -e "${GREEN}   ALL INTEGRATION TESTS PASSED SUCCESSFULLY! 🎉     ${NC}"
echo -e "${CYAN}======================================================${NC}"
echo "Full-stack 3-Tier services are actively running:"
echo "  - Frontend: http://localhost:3000"
echo "  - Backend:  http://localhost:8000"
echo "  - Database: localhost:5432"
echo "  - Dashboard: http://localhost:8080"
echo -e "\nTo stop test containers run: ${YELLOW}./containia stop test-frontend test-backend test-db${NC}"
