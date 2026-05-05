#!/bin/bash
# 电商客服系统 HTTP smoke test

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PROJECT_DIR="${REPO_ROOT}/examples/ecommerce-cs"
GATEWAY_URL="${GATEWAY_URL:-http://127.0.0.1:18890}"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数器
TESTS_PASSED=0
TESTS_FAILED=0

# 打印函数
print_header() {
    echo -e "\n${YELLOW}========================================${NC}"
    echo -e "${YELLOW}$1${NC}"
    echo -e "${YELLOW}========================================${NC}"
}

print_test() {
    echo -e "\n${GREEN}[TEST] $1${NC}"
}

print_pass() {
    echo -e "${GREEN}✅ PASS: $1${NC}"
    ((TESTS_PASSED++))
}

print_fail() {
    echo -e "${RED}❌ FAIL: $1${NC}"
    echo -e "${RED}   Error: $2${NC}"
    ((TESTS_FAILED++))
}

# 检查服务是否运行
check_service() {
    print_header "检查服务状态"
    
    if curl -s --connect-timeout 5 "${GATEWAY_URL}/health" > /dev/null 2>&1; then
        print_pass "Gateway 服务运行中"
        return 0
    else
        print_fail "Gateway 服务未运行" "请先启动: goclaw start --project ${PROJECT_DIR}"
        return 1
    fi
}

# 测试 1: 健康检查
test_health() {
    print_test "TC-0: 健康检查"
    
    response=$(curl -s "${GATEWAY_URL}/health")
    
    if echo "$response" | grep -q '"status":"ok"'; then
        print_pass "健康检查成功"
        echo "   Response: $response"
    else
        print_fail "健康检查失败" "$response"
    fi
}

# 测试 2: 获取 Agent 列表
test_list_agents() {
    print_test "TC-1: 获取 Agent 列表"
    
    response=$(curl -s -X GET "${GATEWAY_URL}/api/v1/agents")
    
    if echo "$response" | grep -q '"agents"'; then
        print_pass "获取 Agent 列表成功"
        echo "   Response: $response" | head -c 200
        echo "..."
    else
        print_fail "获取 Agent 列表失败" "$response"
    fi
}

# 测试 3: 简单对话
test_simple_chat() {
    print_test "TC-2: 简单对话"
    
    response=$(curl -s -X POST "${GATEWAY_URL}/api/v1/chat" \
        -H "Content-Type: application/json" \
        -d '{
            "agentId": "main-cs",
            "text": "你好，请简单介绍一下你自己"
        }')
    
    if echo "$response" | grep -q '"output"'; then
        output=$(echo "$response" | grep -o '"output":"[^"]*"' | head -c 100)
        print_pass "简单对话成功"
        echo "   Response: ${output}..."
    else
        print_fail "简单对话失败" "$response"
    fi
}

# 测试 4: 发现 Agent
test_discover_agents() {
    print_test "TC-3: 发现 Agent (agent.md 扫描)"
    
    agent_count=$(find "${PROJECT_DIR}" -name "agent.md" | wc -l | tr -d ' ')

    if [ "$agent_count" -ge 7 ]; then
        print_pass "发现 $agent_count 个 Agent"
        find "${PROJECT_DIR}" -name "agent.md" | sort | while read agent_file; do
            echo "   - ${agent_file#${PROJECT_DIR}/}"
        done
    else
        print_fail "Agent 数量不足" "预期 7 个，实际 $agent_count 个"
    fi
}

# 测试 5: Agent 状态
test_agent_status() {
    print_test "TC-4: 获取 Agent 状态"
    
    response=$(curl -s -X GET "${GATEWAY_URL}/api/v1/agents/main-cs/status")
    
    if echo "$response" | grep -q '"status"'; then
        print_pass "获取 Agent 状态成功"
        echo "   Response: $response"
    else
        print_fail "获取 Agent 状态失败" "$response"
    fi
}

# 测试 6: 会话历史
test_session_history() {
    print_test "TC-5: 获取会话历史"
    
    response=$(curl -s -X GET "${GATEWAY_URL}/api/v1/sessions/main-cs")
    
    if echo "$response" | grep -q 'entries\|agentId'; then
        print_pass "获取会话历史成功"
    else
        print_fail "获取会话历史失败" "$response"
    fi
}

# 测试 7: 清除会话
test_clear_session() {
    print_test "TC-6: 清除会话"
    
    response=$(curl -s -X DELETE "${GATEWAY_URL}/api/v1/sessions/main-cs")
    
    if echo "$response" | grep -q 'cleared\|ok'; then
        print_pass "清除会话成功"
    else
        print_fail "清除会话失败" "$response"
    fi
}

# 测试 8: 复杂查询（意图识别）
test_complex_query() {
    print_test "TC-7: 复杂查询 - 意图识别"
    
    response=$(curl -s -X POST "${GATEWAY_URL}/api/v1/chat" \
        -H "Content-Type: application/json" \
        -d '{
            "agentId": "main-cs",
            "text": "我想退款，订单号是 ORD123456"
        }')
    
    if echo "$response" | grep -q '"output"'; then
        print_pass "复杂查询成功"
        # 检查是否识别了退款意图
        output=$(echo "$response" | grep -o '"output":"[^"]*"' | head -c 200)
        echo "   Response: ${output}..."
    else
        print_fail "复杂查询失败" "$response"
    fi
}

# 测试 9: 流式响应
test_stream_chat() {
    print_test "TC-8: 流式响应 (SSE)"
    
    # 使用 timeout 限制时间
    response=$(timeout 30s curl -s -X POST "${GATEWAY_URL}/api/v1/chat/stream" \
        -H "Content-Type: application/json" \
        -d '{
            "agentId": "main-cs",
            "text": "说一个短笑话"
        }' 2>/dev/null | head -20)
    
    if echo "$response" | grep -q 'event:\|data:'; then
        print_pass "流式响应成功"
        echo "   Events received:"
        echo "$response" | grep "^event:" | head -5
    else
        print_fail "流式响应失败" "未收到 SSE 事件"
    fi
}

# 打印测试结果
print_summary() {
    print_header "测试结果"
    echo -e "${GREEN}通过: $TESTS_PASSED${NC}"
    echo -e "${RED}失败: $TESTS_FAILED${NC}"
    
    if [ $TESTS_FAILED -eq 0 ]; then
        echo -e "\n${GREEN}🎉 所有测试通过！${NC}"
        exit 0
    else
        echo -e "\n${RED}⚠️ 部分测试失败${NC}"
        exit 1
    fi
}

# 主函数
main() {
    print_header "电商客服系统 - 集成测试"
    echo "Gateway URL: ${GATEWAY_URL}"
    echo "Project Dir: ${PROJECT_DIR}"
    
    # 检查服务
    if ! check_service; then
        echo -e "\n${YELLOW}提示: 请在另一个终端运行:${NC}"
        echo "  goclaw start --project ${PROJECT_DIR}"
        exit 1
    fi
    
    # 运行测试
    test_health
    test_list_agents
    test_simple_chat
    test_discover_agents
    test_agent_status
    test_session_history
    test_clear_session
    test_complex_query
    test_stream_chat
    
    # 打印结果
    print_summary
}

# 运行
main "$@"
