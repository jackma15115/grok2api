export function currentMaterialStatus(material, state = {}) {
  return {
    ...state,
    ready: Boolean(material),
    material,
  };
}
