import { useCallback, useEffect, useRef, useState } from "react";
import type { ModelConfigObject } from "@nex/provider";
import type { ModelInfoLookupResult } from "@nex/services";
import type { ProviderModelDraftValues } from "@/settings/model-provider-section/ProviderModelMetadata.js";

type ModelInfoLookupStatus = "idle" | "loading" | "found" | "not-found" | "failed";

/** 把 models.dev 查询得到的稀疏配置映射为草稿补丁；只写入命中的字段。 */
export function modelInfoDraftPatch(config: ModelConfigObject): Partial<ProviderModelDraftValues> {
  const properties = config.properties;
  const optionSpecs = config.optionSpecs;
  const inputFormat = properties?.inputFormat;
  const patch: Partial<ProviderModelDraftValues> = {};
  if (properties?.contextWindow != null)
    patch.contextWindowValue = String(properties.contextWindow);
  if (optionSpecs?.maxOutputTokens?.max != null)
    patch.maxOutputTokensValue = String(optionSpecs.maxOutputTokens.max);
  if (optionSpecs?.reasoningLevel?.map != null)
    patch.reasoningLevelMapValue = optionSpecs.reasoningLevel.map;
  if (optionSpecs?.reasoningLevel?.values != null)
    patch.reasoningLevelValuesValue = [...optionSpecs.reasoningLevel.values];
  if (inputFormat) {
    patch.inputFormatValue = {
      supportsText: inputFormat.supportsText ?? true,
      supportsImage: inputFormat.supportsImage ?? false,
      supportsVideo: inputFormat.supportsVideo ?? false,
      supportsAudio: inputFormat.supportsAudio ?? false,
      supportsPdf: inputFormat.supportsPdf ?? false,
    };
  }
  if (properties?.supportsJsonSchemaOutput != null)
    patch.supportsJsonSchemaOutputValue = properties.supportsJsonSchemaOutput;
  return patch;
}

/**
 * 模型编辑器"获取模型信息"按钮的状态机。
 *
 * 弹窗关闭或模型 ID 变化时丢弃进行中的请求并清空反馈：旧 ID 的回包不能
 * 填进新 ID 的表单，"已填入"提示也不能残留到另一个模型上。
 */
export function useModelInfoLookup({
  open,
  modelId,
  lookup,
  onApply,
}: {
  open: boolean;
  modelId: string;
  lookup?: (modelId: string) => Promise<ModelInfoLookupResult>;
  onApply: (patch: Partial<ProviderModelDraftValues>) => void;
}) {
  const [status, setStatus] = useState<ModelInfoLookupStatus>("idle");
  const generationRef = useRef(0);
  const normalizedModelId = modelId.trim();

  useEffect(() => {
    generationRef.current += 1;
    setStatus("idle");
  }, [open, normalizedModelId]);

  useEffect(
    () => () => {
      generationRef.current += 1;
    },
    [],
  );

  const run = useCallback(async () => {
    if (!lookup || !normalizedModelId) return;
    const generation = ++generationRef.current;
    const isCurrent = () => generationRef.current === generation;
    setStatus("loading");
    try {
      const result = await lookup(normalizedModelId);
      if (!isCurrent()) return;
      if (!result.found) {
        setStatus("not-found");
        return;
      }
      onApply(modelInfoDraftPatch(result.config));
      setStatus("found");
    } catch {
      if (isCurrent()) setStatus("failed");
    }
  }, [lookup, normalizedModelId, onApply]);

  return { status, run, available: Boolean(lookup) } as const;
}
