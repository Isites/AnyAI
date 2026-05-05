#!/bin/bash
# 电商客服项目结构 smoke test

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PROJECT_DIR="${REPO_ROOT}/examples/ecommerce-cs"

echo "=== 电商客服系统 - 项目结构检查 ==="
echo ""

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

PASS=0
FAIL=0

echo "测试 1: 项目配置文件"
if [ -f "${PROJECT_DIR}/anyai.yaml" ]; then
    echo -e "${GREEN}✅ anyai.yaml 存在${NC}"
    ((PASS++))
else
    echo -e "${RED}❌ anyai.yaml 不存在${NC}"
    ((FAIL++))
fi

echo ""
echo "测试 2: 根入口 agent.md"
if [ -f "${PROJECT_DIR}/agent.md" ]; then
    echo -e "${GREEN}✅ 根入口 agent.md 存在${NC}"
    ((PASS++))
else
    echo -e "${RED}❌ 根入口 agent.md 不存在${NC}"
    ((FAIL++))
fi

echo ""
echo "测试 3: Agent 数量"
agent_count=$(find "${PROJECT_DIR}" -name "agent.md" | wc -l | tr -d ' ')
if [ "$agent_count" -ge 7 ]; then
    echo -e "${GREEN}✅ 找到 $agent_count 个 agent.md 文件${NC}"
    find "${PROJECT_DIR}" -name "agent.md" | sort | while read agent_file; do
        echo "   - ${agent_file#${PROJECT_DIR}/}"
    done
    ((PASS++))
else
    echo -e "${RED}❌ agent.md 文件数量不足 (预期 7，实际 $agent_count)${NC}"
    ((FAIL++))
fi

echo ""
echo "测试 4: frontmatter 基本字段"
all_valid=true
for agent_file in $(find "${PROJECT_DIR}" -name "agent.md" | sort); do
    agent_name=$(basename "$(dirname "$agent_file")")
    if head -1 "$agent_file" | grep -q "^---$" && grep -q "^name:" "$agent_file"; then
        echo -e "   ${GREEN}✓${NC} $agent_name"
    else
        echo -e "   ${RED}✗${NC} $agent_name"
        all_valid=false
    fi
done

if [ "$all_valid" = true ]; then
    echo -e "${GREEN}✅ 所有 agent.md 都包含基本 frontmatter${NC}"
    ((PASS++))
else
    echo -e "${RED}❌ 存在格式不完整的 agent.md${NC}"
    ((FAIL++))
fi

echo ""
echo "测试 5: 数据文件"
data_files=("orders.json" "inventory.json" "users.json")
all_exist=true
for file in "${data_files[@]}"; do
    if [ -f "${PROJECT_DIR}/data/$file" ]; then
        echo -e "   ${GREEN}✓${NC} $file"
    else
        echo -e "   ${RED}✗${NC} $file"
        all_exist=false
    fi
done

if [ "$all_exist" = true ]; then
    echo -e "${GREEN}✅ 所有测试数据文件存在${NC}"
    ((PASS++))
else
    echo -e "${RED}❌ 部分测试数据文件缺失${NC}"
    ((FAIL++))
fi

echo ""
echo "测试 6: 私有技能"
if [ -f "${PROJECT_DIR}/agents/logistics-specialist/skills/logistics/SKILL.md" ]; then
    echo -e "${GREEN}✅ 物流专员私有技能存在${NC}"
    ((PASS++))
else
    echo -e "${RED}❌ 物流专员私有技能缺失${NC}"
    ((FAIL++))
fi

# 总结
echo ""
echo "=== 测试结果 ==="
echo -e "${GREEN}通过: $PASS${NC}"
echo -e "${RED}失败: $FAIL${NC}"

if [ $FAIL -eq 0 ]; then
    echo -e "\n${GREEN}🎉 所有测试通过！${NC}"
    exit 0
else
    echo -e "\n${RED}⚠️ 部分测试失败${NC}"
    exit 1
fi
