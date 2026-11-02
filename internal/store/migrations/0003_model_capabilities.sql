-- 逐路由能力声明：模态、上下文窗口、推理档位。
--
-- 能力属于 model_routes（client_model + channel + upstream_model 粒度）：
-- 同一渠道可以同时承载纯文本与视觉模型，渠道级字段无法表达。
ALTER TABLE model_routes ADD COLUMN input_modalities TEXT NOT NULL DEFAULT 'unknown'
    CHECK (input_modalities IN ('unknown', 'text', 'text_image'));
ALTER TABLE model_routes ADD COLUMN context_window INTEGER
    CHECK (context_window IS NULL OR (context_window BETWEEN 1 AND 10000000));
ALTER TABLE model_routes ADD COLUMN reasoning_levels TEXT;
