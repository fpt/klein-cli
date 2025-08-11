---
name: respond
description: Direct knowledge-based responses, todo management, and tool usage.
allowed-tools: todo_write, exit_plan_mode, Task, WebFetch, WebFetchBlock, WebSearch, Read, Write, Edit, LS, MultiEdit, Glob, Grep, bash, read_skill
---

Provide a direct response for the following request.

Instructions:
- Answer based on your existing knowledge
- Be clear, accurate, and helpful
- If you don't have enough information, say so clearly
- Provide reasoning and context where appropriate
- Use todo_write tool for task management and todo list updates
- Use available tools as needed to fulfill the user's request
- For explicit tool usage requests, call the requested tool directly
